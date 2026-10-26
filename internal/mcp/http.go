package mcp

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/payram/payram-analytics-mcp-server/internal/listen"
	"github.com/payram/payram-analytics-mcp-server/internal/logging"
	"github.com/payram/payram-analytics-mcp-server/internal/protocol"
	"github.com/payram/payram-analytics-mcp-server/internal/version"
	"github.com/sirupsen/logrus"
)

// RunHTTP starts an HTTP server that serves MCP JSON-RPC requests via POST.
// Expects a single JSON-RPC request per call. Clients should POST to the root path.
func RunHTTP(server *Server, addr string) error {
	logger, cleanup, err := logging.New("mcp-http")
	if err != nil {
		return err
	}
	defer cleanup()

	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	http.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(version.Get())
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()

		if r.Method != http.MethodPost {
			rec.WriteHeader(http.StatusMethodNotAllowed)
			logger.WithFields(logrus.Fields{"method": r.Method, "status": rec.status}).Warn("method not allowed")
			return
		}

		var req protocol.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logger.WithError(err).Warn("invalid JSON")
			writeJSON(rec, protocol.Response{Error: &protocol.ResponseError{Code: -32700, Message: "invalid JSON"}}, http.StatusBadRequest)
			logRequest(logger, r, rec, start)
			return
		}

		resp, err := server.Handle(r.Context(), req)
		if err != nil {
			logger.WithError(err).Error("mcp handler error")
			writeJSON(rec, WriteError(req.ID, -32603, "internal error", err), http.StatusInternalServerError)
			logRequest(logger, r, rec, start)
			return
		}

		writeJSON(rec, resp, http.StatusOK)
		logRequest(logger, r, rec, start)
	})

	if warning := listen.ExposureWarning("MCP server", addr); warning != "" {
		logger.Warn(warning)
	}
	logger.Infof("HTTP MCP server listening on %s", addr)
	return http.ListenAndServe(addr, nil)
}

func writeJSON(w http.ResponseWriter, resp protocol.Response, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(resp)
}

type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *responseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func logRequest(logger *logrus.Entry, r *http.Request, rec *responseRecorder, start time.Time) {
	logger.WithFields(logrus.Fields{
		"method": r.Method,
		"path":   r.URL.Path,
		"status": rec.status,
		"bytes":  rec.bytes,
		"dur":    time.Since(start).Round(time.Millisecond),
	}).Info("request")
}
