package kernel

import (
	"context"
	"log/slog"

	"github.com/rebuno/rebuno/internal/auth"
)

func (k *Kernel) securityLog(ctx context.Context, action string, attrs ...slog.Attr) {
	if k.d.SecurityLog == nil {
		return
	}
	principal := auth.Identity(ctx)
	if principal == "" {
		principal = "anonymous"
	}
	k.d.SecurityLog.LogAttrs(ctx, slog.LevelInfo, action, append([]slog.Attr{slog.String("principal", principal)}, attrs...)...)
}

func (k *Kernel) LogDenied(ctx context.Context, method, path string, status int) {
	k.securityLog(ctx, "request.denied", slog.String("method", method), slog.String("path", path), slog.Int("status", status))
}
