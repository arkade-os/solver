package grpcservice

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	swapv1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
	"github.com/arkade-os/solver/internal/interface/web"
	log "github.com/sirupsen/logrus"
)

const maxRequestBodySize = 1 << 20 // 1 MB

// Server manages the gRPC server and HTTP REST gateway.
type Server struct {
	grpcServer *grpc.Server
	httpServer *http.Server
	handler    *handler
	grpcPort   int
	httpPort   int
}

// NewServer creates a new Server that serves both gRPC and HTTP for the swap
// service.
func NewServer(
	grpcPort, httpPort int,
	svc SwapService,
) *Server {
	return &Server{
		grpcPort: grpcPort,
		httpPort: httpPort,
		handler:  newHandler(svc),
	}
}

// Start starts both the gRPC server and the HTTP gateway.
func (s *Server) Start() error {
	// Start gRPC server
	grpcListener, err := net.Listen("tcp", fmt.Sprintf(":%d", s.grpcPort))
	if err != nil {
		return fmt.Errorf("failed to listen on gRPC port %d: %w", s.grpcPort, err)
	}

	s.grpcServer = grpc.NewServer()
	swapv1.RegisterSwapServiceServer(s.grpcServer, s.handler)
	swapv1.RegisterWalletServiceServer(s.grpcServer, s.handler)

	go func() {
		log.Infof("gRPC server listening on :%d", s.grpcPort)
		if err := s.grpcServer.Serve(grpcListener); err != nil {
			log.WithError(err).Error("gRPC server failed")
		}
	}()

	// Start HTTP gateway
	mux := http.NewServeMux()
	gwHandler := newHTTPGateway(s.handler)
	mux.Handle("/v1/", gwHandler)
	mux.Handle("/", web.Handler())

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.httpPort),
		Handler: mux,
	}

	go func() {
		log.Infof("HTTP gateway listening on :%d", s.httpPort)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.WithError(err).Error("HTTP gateway failed")
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
			log.WithError(err).Error("failed to shutdown HTTP server")
		}
	}
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
	log.Info("servers stopped")
}

// newHTTPGateway creates a simple HTTP handler that routes REST requests
// to the gRPC handlers. This is a lightweight alternative to grpc-gateway
// that avoids the full protobuf dependency for hand-written types.
func newHTTPGateway(svc *handler) http.Handler {
	mux := http.NewServeMux()
	registerSwapRoutes(mux, svc)
	return mux
}

func registerSwapRoutes(mux *http.ServeMux, svc *handler) {
	mux.HandleFunc("POST /v1/market", func(w http.ResponseWriter, r *http.Request) {
		var req swapv1.AddMarketRequest
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, fmt.Errorf("invalid request body: %w", err), http.StatusBadRequest)
			return
		}
		resp, err := svc.AddMarket(r.Context(), &req)
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("PUT /v1/market", func(w http.ResponseWriter, r *http.Request) {
		var req swapv1.UpdateMarketRequest
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, fmt.Errorf("invalid request body: %w", err), http.StatusBadRequest)
			return
		}
		resp, err := svc.UpdateMarket(r.Context(), &req)
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("DELETE /v1/market/{base}/{quote}", func(w http.ResponseWriter, r *http.Request) {
		base := r.PathValue("base")
		quote := r.PathValue("quote")
		resp, err := svc.RemoveMarket(r.Context(), &swapv1.RemoveMarketRequest{BaseAsset: base, QuoteAsset: quote})
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("GET /v1/markets", func(w http.ResponseWriter, r *http.Request) {
		resp, err := svc.ListMarkets(r.Context(), &swapv1.ListMarketsRequest{})
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		resp, err := svc.GetStatus(r.Context(), &swapv1.GetStatusRequest{})
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("GET /v1/balance", func(w http.ResponseWriter, r *http.Request) {
		resp, err := svc.GetBalance(r.Context(), &swapv1.GetBalanceRequest{})
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("GET /v1/address", func(w http.ResponseWriter, r *http.Request) {
		resp, err := svc.GetAddress(r.Context(), &swapv1.GetAddressRequest{})
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
		status := r.URL.Query().Get("status")
		if status != "failed" && status != "succeeded" {
			status = ""
		}
		resp, err := svc.ListTrades(r.Context(), &swapv1.ListTradesRequest{
			Limit:  limit,
			Status: status,
		})
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("GET /v1/assets", func(w http.ResponseWriter, r *http.Request) {
		resp, err := svc.ListAssets(r.Context(), &swapv1.ListAssetsRequest{})
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("POST /v1/wallet/send", func(w http.ResponseWriter, r *http.Request) {
		var req swapv1.SendOffchainRequest
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, fmt.Errorf("invalid request body: %w", err), http.StatusBadRequest)
			return
		}
		resp, err := svc.SendOffchain(r.Context(), &req)
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("POST /v1/wallet/exit", func(w http.ResponseWriter, r *http.Request) {
		var req swapv1.CollaborativeExitRequest
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, fmt.Errorf("invalid request body: %w", err), http.StatusBadRequest)
			return
		}
		resp, err := svc.CollaborativeExit(r.Context(), &req)
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, resp)
	})

	mux.HandleFunc("GET /v1/config", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, svc.GetConfig())
	})

	mux.HandleFunc("POST /v1/wallet/dump", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Password string `json:"password"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, fmt.Errorf("invalid request body: %w", err), http.StatusBadRequest)
			return
		}
		seed, err := svc.DumpSeed(r.Context(), req.Password)
		if err != nil {
			httpGRPCError(w, err)
			return
		}
		jsonResponse(w, map[string]string{"seed": seed})
	})

	mux.HandleFunc("POST /v1/wallet/settle", func(w http.ResponseWriter, r *http.Request) {
		var req swapv1.SettleRequest
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, fmt.Errorf("invalid request body: %w", err), http.StatusBadRequest)
			return
		}
		resp, err := svc.Settle(r.Context(), &req)
		if err != nil {
			httpGRPCError(w, err)
			return
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
	case codes.Unauthenticated:
		httpCode = http.StatusUnauthorized
	case codes.AlreadyExists:
		httpCode = http.StatusConflict
	case codes.Unimplemented:
		httpCode = http.StatusNotImplemented
	}
	httpError(w, err, httpCode)
}
