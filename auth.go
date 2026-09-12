package main

import (
	"context"
	"fmt"
	"net/http"

	pkgerrors "github.com/pkg/errors"
	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const UserContextKey contextKey = "user"

var allowedUsers = map[string]string{
	"frodo":   "$2a$10$B6O/n6teuCzpuh66jrUAdeaJ3WvXcxRkzpN0x7H.di9G9e/NGb9Me",
	"samwise": "$2a$10$EWZpvYhUJtJcEMmm/IBOsOGIcpxUnGIVMRiDlN/nxl1RRwWGkJtty",
	// frodo: "ofTheNineFingers"
	// samwise: "theStrong"
	"saruman": "invalidFormat",
}

func (s *server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok {
			httpError(r.Context(), w, http.StatusUnauthorized, fmt.Errorf("unauthorized"))
			return
		}
		stored, exists := allowedUsers[username]
		if !exists {
			httpError(r.Context(), w, http.StatusUnauthorized, fmt.Errorf("unauthorized"))
			return
		}

		ctx, span := tracer.Start(r.Context(), "auth.validate_password")
		ok, err := s.validatePassword(password, stored)
		span.End()
		if err != nil {
			httpError(ctx, w, http.StatusInternalServerError, fmt.Errorf("internal server error: %w", err))
			return
		}
		if !ok {
			httpError(ctx, w, http.StatusUnauthorized, fmt.Errorf("unauthorized"))
			return
		}
		r = r.WithContext(context.WithValue(ctx, UserContextKey, username))

		logContext, ok := ctx.Value(logContextKey).(*LogContext)

		if ok {
			logContext.Username = username
		}

		next.ServeHTTP(w, r)
	})
}

func (s *server) validatePassword(password, stored string) (bool, error) {
	err := bcrypt.CompareHashAndPassword([]byte(stored), []byte(password))
	if err == bcrypt.ErrMismatchedHashAndPassword {
		return false, nil
	}
	if err != nil {
		return false, pkgerrors.WithStack(err)
	}
	return true, nil
}
