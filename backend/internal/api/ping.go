// Package api holds the RPC service implementations and HTTP middleware the
// backend serves.
package api

import (
	"context"

	"connectrpc.com/connect"

	lemmyschedulerv1 "github.com/gumert/LemmyScheduler/backend/internal/gen/lemmyscheduler/v1"
	"github.com/gumert/LemmyScheduler/backend/internal/gen/lemmyscheduler/v1/lemmyschedulerv1connect"
)

// PingMessage is the static message returned by PingService.Ping.
const PingMessage = "pong"

// PingServer implements lemmyschedulerv1connect.PingServiceHandler.
type PingServer struct{}

var _ lemmyschedulerv1connect.PingServiceHandler = PingServer{}

// Ping returns a static message.
func (PingServer) Ping(_ context.Context, _ *connect.Request[lemmyschedulerv1.PingRequest]) (*connect.Response[lemmyschedulerv1.PingResponse], error) {
	return connect.NewResponse(&lemmyschedulerv1.PingResponse{Message: PingMessage}), nil
}
