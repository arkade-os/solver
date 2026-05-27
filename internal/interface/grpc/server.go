package grpcservice

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	bancov1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
	"github.com/arkade-os/solver/internal/core/application"
	"github.com/arkade-os/solver/internal/interface/web"
)

const maxRequestBodySize = 1 << 20 // 1 MB

// Server manages the gRPC server and HTTP REST gateway.
type Server struct {
	grpcServer      *grpc.Server
	httpServer      *http.Server
	grpcConn        *grpc.ClientConn
	handler         bancov1.BancoServiceServer    // nil when banco is disabled
	preimageHandler bancov1.PreimageServiceServer // nil when preimage is disabled
	bancoSvc        *application.TakerService     // nil when banco is disabled
	preimageSvc     *application.PreimageService  // nil when preimage is disabled
	grpcPort        int
	httpPort        int
	log             logrus.FieldLogger
}

// NewServer creates a new Server that serves both gRPC and HTTP. svc may be
// nil when the banco plugin is disabled.
func NewServer(
	svc *application.TakerService,
	grpcPort, httpPort int,
	log logrus.FieldLogger,
) *Server {
	s := &Server{
		grpcPort: grpcPort,
		httpPort: httpPort,
		log:      log,
	}
	if svc != nil {
		s.handler = newHandler(svc)
		s.bancoSvc = svc
	}
	return s
}

// WithPreimageService attaches the preimage handler so its RPCs are exposed
// alongside the banco handler. Pass nil to leave it disabled.
func (s *Server) WithPreimageService(svc *application.PreimageService) *Server {
	if svc != nil {
		s.preimageHandler = newPreimageHandler(svc)
		s.preimageSvc = svc
	}
	return s
}

// Start starts both the gRPC server and the HTTP gateway.
func (s *Server) Start() error {
	// Start gRPC server
	grpcListener, err := net.Listen("tcp", fmt.Sprintf(":%d", s.grpcPort))
	if err != nil {
		return fmt.Errorf("failed to listen on gRPC port %d: %w", s.grpcPort, err)
	}

	s.grpcServer = grpc.NewServer()
	if s.handler != nil {
		bancov1.RegisterBancoServiceServer(s.grpcServer, s.handler)
	}
	if s.preimageHandler != nil {
		bancov1.RegisterPreimageServiceServer(s.grpcServer, s.preimageHandler)
	}

	go func() {
		s.log.Infof("gRPC server listening on :%d", s.grpcPort)
		if err := s.grpcServer.Serve(grpcListener); err != nil {
			s.log.WithError(err).Error("gRPC server failed")
		}
	}()

	// Start HTTP gateway
	grpcAddr := fmt.Sprintf("localhost:%d", s.grpcPort)
	conn, err := grpc.NewClient(
		grpcAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("failed to dial gRPC server: %w", err)
	}
	s.grpcConn = conn

	mux := http.NewServeMux()
	gwHandler := newHTTPGateway(conn, s.handler, s.preimageHandler)
	mux.Handle("/v1/plugins", s.pluginsHandler())
	mux.Handle("/v1/", gwHandler)
	mux.Handle("/", web.Handler())

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.httpPort),
		Handler: mux,
	}

	go func() {
		s.log.Infof("HTTP gateway listening on :%d", s.httpPort)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.WithError(err).Error("HTTP gateway failed")
		}
	}()

	return nil
}

// Stop gracefully shuts down both the gRPC server and the HTTP gateway.
func (s *Server) Stop() {
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(ctx); err != nil {
			s.log.WithError(err).Error("failed to shutdown HTTP server")
		}
	}
	if s.grpcConn != nil {
		if err := s.grpcConn.Close(); err != nil {
			s.log.WithError(err).Error("failed to close gRPC connection")
		}
	}
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
	s.log.Info("servers stopped")
}

// newHTTPGateway creates a simple HTTP handler that routes REST requests
// to the gRPC handlers. This is a lightweight alternative to grpc-gateway
// that avoids the full protobuf dependency for hand-written types.
//
// Either service may be nil; routes are registered only for the services
// that are present.
func newHTTPGateway(
	_ *grpc.ClientConn,
	svc bancov1.BancoServiceServer,
	preimageSvc bancov1.PreimageServiceServer,
) http.Handler {
	mux := http.NewServeMux()
	if svc != nil {
		registerBancoRoutes(mux, svc)
	}
	if preimageSvc != nil {
		registerPreimageRoutes(mux, preimageSvc)
	}
	return mux
}

func registerBancoRoutes(mux *http.ServeMux, svc bancov1.BancoServiceServer) {
	mux.HandleFunc("POST /v1/pair", func(w http.ResponseWriter, r *http.Request) {
		var req bancov1.AddPairRequest
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, fmt.Errorf("invalid request body: %w", err), http.StatusBadRequest)
			return
		}
		resp, err := svc.AddPair(r.Context(), &req)
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("PUT /v1/pair", func(w http.ResponseWriter, r *http.Request) {
		var req bancov1.UpdatePairRequest
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, fmt.Errorf("invalid request body: %w", err), http.StatusBadRequest)
			return
		}
		resp, err := svc.UpdatePair(r.Context(), &req)
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("DELETE /v1/pair/{pair}", func(w http.ResponseWriter, r *http.Request) {
		pairName := r.PathValue("pair")
		resp, err := svc.RemovePair(r.Context(), &bancov1.RemovePairRequest{Pair: pairName})
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("GET /v1/pairs", func(w http.ResponseWriter, r *http.Request) {
		resp, err := svc.ListPairs(r.Context(), &bancov1.ListPairsRequest{})
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		resp, err := svc.GetStatus(r.Context(), &bancov1.GetStatusRequest{})
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("GET /v1/balance", func(w http.ResponseWriter, r *http.Request) {
		resp, err := svc.GetBalance(r.Context(), &bancov1.GetBalanceRequest{})
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("GET /v1/address", func(w http.ResponseWriter, r *http.Request) {
		resp, err := svc.GetAddress(r.Context(), &bancov1.GetAddressRequest{})
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("GET /v1/trades", func(w http.ResponseWriter, r *http.Request) {
		var limit int32
		if q := r.URL.Query().Get("limit"); q != "" {
			if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 1000 {
				limit = int32(n) //nolint:gosec
			}
		}
		resp, err := svc.ListTrades(r.Context(), &bancov1.ListTradesRequest{Limit: limit})
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})
}

func registerPreimageRoutes(mux *http.ServeMux, svc bancov1.PreimageServiceServer) {
	mux.HandleFunc("GET /v1/preimage/solver-pubkey", func(w http.ResponseWriter, r *http.Request) {
		resp, err := svc.GetSolverPubKey(r.Context(), &bancov1.GetSolverPubKeyRequest{})
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})
}

// pluginsHandler reports which plugins are enabled and whether they are running.
func (s *Server) pluginsHandler() http.Handler {
	type pluginState struct {
		Enabled bool `json:"enabled"`
		Running bool `json:"running"`
	}
	type pluginsResponse struct {
		Banco    pluginState `json:"banco"`
		Preimage pluginState `json:"preimage"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpError(w, fmt.Errorf("method not allowed"), http.StatusMethodNotAllowed)
			return
		}
		resp := pluginsResponse{}
		if s.bancoSvc != nil {
			resp.Banco.Enabled = true
			resp.Banco.Running = s.bancoSvc.Status().Running
		}
		if s.preimageSvc != nil {
			resp.Preimage.Enabled = true
			resp.Preimage.Running = s.preimageSvc.Status().Running
		}
		jsonResponse(w, resp)
	})
}

func jsonResponse(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func httpError(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
}

// httpGRPCError maps gRPC status codes to HTTP status codes.
func httpGRPCError(w http.ResponseWriter, err error) {
	st, ok := status.FromError(err)
	if !ok {
		httpError(w, err, http.StatusInternalServerError)
		return
	}
	httpCode := http.StatusInternalServerError
	switch st.Code() {
	case codes.InvalidArgument:
		httpCode = http.StatusBadRequest
	case codes.NotFound:
		httpCode = http.StatusNotFound
	case codes.AlreadyExists:
		httpCode = http.StatusConflict
	case codes.Unimplemented:
		httpCode = http.StatusNotImplemented
	}
	httpError(w, err, httpCode)
}
