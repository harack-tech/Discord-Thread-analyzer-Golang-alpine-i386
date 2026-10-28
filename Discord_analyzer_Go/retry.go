package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/bwmarrin/discordgo"
)

const retryAttempts = 5

func retry(ctx context.Context, operation string, isRetryable func(error) bool, run func() error) error {
	delay := time.Second
	var lastErr error
	for attempt := 1; attempt <= retryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = run()
		if lastErr == nil {
			return nil
		}
		if attempt == retryAttempts || !isRetryable(lastErr) {
			break
		}
		logger.Printf("%s に失敗しました (%d/%d): %v。%s後に再試行します", operation, attempt, retryAttempts, lastErr, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
		delay *= 2
		if delay > 16*time.Second {
			delay = 16 * time.Second
		}
	}
	return lastErr
}

func retryableHTTPError(err error) bool {
	var apiErr *httpStatusError
	if errors.As(err, &apiErr) {
		return apiErr.status == http.StatusRequestTimeout ||
			apiErr.status == http.StatusTooManyRequests ||
			apiErr.status >= 500
	}
	return retryableDiscordError(err)
}

func retryableDiscordError(err error) bool {
	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) && restErr.Response != nil {
		status := restErr.Response.StatusCode
		return status == http.StatusRequestTimeout ||
			status == http.StatusTooManyRequests ||
			status >= 500
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
