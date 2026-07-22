// Package tunnel bridges a local TCP connection (Testcontainers/Docker SDK
// talking to what it thinks is a local Docker daemon) to the control
// plane's WebSocket relay, which in turn is the only thing that can reach
// the actual VM's dockerd (see devplat-backend/src/routes/tunnel.ts for why:
// the VM only accepts connections from the control plane's own WireGuard
// subnet, which this process is never on).
package tunnel

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
)

// Bridge copies bytes both ways between local (an accepted TCP connection
// from the Docker client) and a fresh WebSocket connection to
// {apiURL}/environments/{requestID}/tunnel until either side closes. Blocks
// until the connection ends; callers run it in its own goroutine per
// accepted local connection.
func Bridge(apiURL, token, requestID string, local io.ReadWriteCloser) error {
	return bridge(apiURL, token, requestID, 0, local)
}

// BridgePort is Bridge for one container port published inside the remote
// VM, connecting to {apiURL}/environments/{requestID}/tunnel/{port} instead
// of the Docker API tunnel. Used by internal/portwatch: one bridge per
// accepted local connection on a mirrored port, exactly like Bridge is one
// per local Docker API connection.
func BridgePort(apiURL, token, requestID string, port int, local io.ReadWriteCloser) error {
	return bridge(apiURL, token, requestID, port, local)
}

func bridge(apiURL, token, requestID string, port int, local io.ReadWriteCloser) error {
	wsURL, err := toWebsocketURL(apiURL, requestID, port)
	if err != nil {
		return err
	}
	header := http.Header{"Authorization": {"Bearer " + token}}
	ws, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("tunnel dial failed: %w (status %d)", err, resp.StatusCode)
		}
		return fmt.Errorf("tunnel dial failed: %w", err)
	}
	defer ws.Close()

	errc := make(chan error, 2)

	// local -> ws
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := local.Read(buf)
			if n > 0 {
				if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					errc <- werr
					return
				}
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()

	// ws -> local
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if _, werr := local.Write(data); werr != nil {
				errc <- werr
				return
			}
		}
	}()

	err = <-errc
	local.Close()
	// CloseNoStatusReceived (1005): RFC 6455's code for "the peer closed
	// without sending a status" — exactly what a bare, argument-less
	// WebSocket .close() on the relay's side produces. For this raw byte
	// pipe (no higher-level protocol negotiating close reasons) that's just
	// as much a normal end-of-connection as 1000/1001, so it's tolerated the
	// same way rather than logged as a tunnel error.
	if err == io.EOF || websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
		return nil
	}
	return err
}

// toWebsocketURL builds the tunnel URL; port 0 means the Docker API tunnel,
// any other port the per-container-port tunnel.
func toWebsocketURL(apiURL, requestID string, port int) (string, error) {
	u, err := url.Parse(apiURL)
	if err != nil {
		return "", fmt.Errorf("invalid API URL %q: %w", apiURL, err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported API URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/environments/" + requestID + "/tunnel"
	if port != 0 {
		u.Path += "/" + strconv.Itoa(port)
	}
	return u.String(), nil
}
