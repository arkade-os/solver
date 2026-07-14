package e2e_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	swapv1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
)

const e2eGRPCAddr = "localhost:7170"

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// runTests prepares arkd funds, waits for the dockerized solverd, and funds the
// solver before running the suite.
func runTests(m *testing.M) int {
	log.SetLevel(log.DebugLevel)
	ctx := context.Background()

	if err := refillArkd(ctx); err != nil {
		log.Errorf("failed to refill arkd: %s", err)
		return 1
	}

	if err := waitSwapReady(ctx); err != nil {
		log.Errorf("failed waiting for solverd readiness: %s", err)
		return 1
	}

	if err := fundSolver(ctx); err != nil {
		log.Errorf("failed to fund solver: %s", err)
		return 1
	}

	return m.Run()
}

func waitSwapReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	conn, err := grpc.NewClient(e2eGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	// nolint:errcheck
	defer conn.Close()
	client := swapv1.NewSwapServiceClient(conn)

	for {
		callCtx, callCancel := context.WithTimeout(ctx, time.Second)
		resp, err := client.GetStatus(callCtx, &swapv1.GetStatusRequest{})
		callCancel()
		if err == nil && resp.GetRunning() {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for solverd: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func refillArkd(ctx context.Context) error {
	arkdExec := "docker exec solverd-arkd arkd"
	command := fmt.Sprintf("%s wallet balance", arkdExec)
	out, err := runCommand(ctx, command)
	if err != nil {
		return err
	}
	re := regexp.MustCompile(`available:\s*([0-9]+\.[0-9]+)`)
	matches := re.FindStringSubmatch(out)
	if len(matches) < 2 {
		return fmt.Errorf("could not parse arkd balance from: %s", out)
	}
	balance, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return err
	}
	if delta := 5.0 - balance; delta >= 1 {
		addrCmd := fmt.Sprintf("%s wallet address", arkdExec)
		address, err := runCommand(ctx, addrCmd)
		if err != nil {
			return err
		}
		for range int(delta) {
			if err := faucet(ctx, strings.TrimSpace(address), 1); err != nil {
				return err
			}
		}
	}
	time.Sleep(5 * time.Second)
	return nil
}
