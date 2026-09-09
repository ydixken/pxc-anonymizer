// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

package objectstore

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Transport shares verified TLS and namespace-local CA resolution with the HTTP pointer reader.
func Transport(ctx context.Context, reader client.Reader, namespace string, ca *corev1.SecretKeySelector, insecure bool) (*http.Transport, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport must support cloning")
	}
	transport := base.Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		// This bypass is available only through the explicit API field.
		InsecureSkipVerify: insecure, // #nosec G402
	}
	if ca == nil {
		return transport, nil
	}
	if reader == nil || namespace == "" || ca.Name == "" || ca.Key == "" {
		return nil, errors.New("custom CA requires a reader, namespace, Secret name and key")
	}
	var secret corev1.Secret
	if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ca.Name}, &secret); err != nil {
		return nil, fmt.Errorf("read CA Secret: %w", err)
	}
	certificates := secret.Data[ca.Key]
	if len(certificates) == 0 {
		return nil, errors.New("CA Secret key must contain a PEM certificate bundle")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system CA certificates: %w", err)
	}
	if !roots.AppendCertsFromPEM(certificates) {
		return nil, errors.New("CA Secret key contains no PEM certificates")
	}
	transport.TLSClientConfig.RootCAs = roots
	return transport, nil
}
