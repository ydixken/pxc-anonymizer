// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package pointer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/ydixken/pxc-anonymizer/api/v1alpha1"
	"github.com/ydixken/pxc-anonymizer/internal/objectstore"
)

type Fetcher struct {
	Reader    client.Reader
	Namespace string
}

func (f Fetcher) Fetch(ctx context.Context, source api.PointerSource) (*Document, error) {
	if (source.HTTP == nil) == (source.S3 == nil) {
		return nil, errors.New("set exactly one of http or s3")
	}
	if source.HTTP != nil {
		return f.fetchHTTP(ctx, *source.HTTP)
	}
	store, err := objectstore.Resolve(ctx, f.Reader, f.Namespace, source.S3.ObjectStorage)
	if err != nil {
		return nil, err
	}
	var document Document
	if err := store.GetJSON(ctx, source.S3.Key, &document); err != nil {
		return nil, err
	}
	return &document, nil
}

func (f Fetcher) fetchHTTP(ctx context.Context, source api.HTTPPointerSource) (*Document, error) {
	u, err := objectstore.ParseURL(source.URL, source.AllowInsecure)
	if err != nil {
		return nil, err
	}
	timeout := 30 * time.Second
	if source.Timeout != nil {
		timeout = source.Timeout.Duration
	}
	if timeout <= 0 {
		return nil, errors.New("HTTP pointer timeout must be positive")
	}
	maxBytes := source.MaxBytes
	if maxBytes == 0 {
		maxBytes = objectstore.MaxJSONBytes
	}
	if maxBytes < 1 || maxBytes == int64(^uint64(0)>>1) {
		return nil, errors.New("HTTP pointer maxBytes must be positive and leave room for an overflow byte")
	}
	transport, err := objectstore.Transport(ctx, f.Reader, f.Namespace, source.CASecretRef, false)
	if err != nil {
		return nil, err
	}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{
		Timeout: timeout, Transport: transport,
		CheckRedirect: func(request *http.Request, previous []*http.Request) error {
			if len(previous) >= 10 {
				return errors.New("too many pointer redirects")
			}
			_, err := objectstore.ParseURL(request.URL.String(), source.AllowInsecure)
			return err
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid HTTP pointer request")
	}
	response, err := httpClient.Do(request)
	if err != nil {
		if requestError, ok := errors.AsType[*url.Error](err); ok {
			err = requestError.Err
		}
		return nil, fmt.Errorf("fetch HTTP pointer: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return nil, objectstore.ErrNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch HTTP pointer: status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read HTTP pointer: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("HTTP pointer exceeds maxBytes")
	}
	return Decode(data)
}
