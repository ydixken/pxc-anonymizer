// Copyright 2026 pxc-anonymizer contributors.
// SPDX-License-Identifier: Apache-2.0

// Package pointer reads legacy pointers and publishes additive schema-v2 documents.
package pointer

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"time"

	"github.com/ydixken/pxc-anonymizer/internal/objectstore"
)

const SchemaVersion = 2

type Document struct {
	Name          string         `json:"name"`
	Destination   string         `json:"destination"`
	SchemaVersion int            `json:"schemaVersion"`
	PublishedAt   *time.Time     `json:"publishedAt,omitempty"`
	PublishedBy   *Publisher     `json:"publishedBy,omitempty"`
	SourceCluster *SourceCluster `json:"sourceCluster,omitempty"`
	Backup        *Backup        `json:"backup,omitempty"`
	S3            *S3            `json:"s3,omitempty"`
	Anonymized    *Anonymized    `json:"anonymized,omitempty"`
	PublicURL     string         `json:"publicURL,omitempty"`
}

type Publisher struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

type SourceCluster struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	CRVersion string `json:"crVersion,omitempty"`
}

type Backup struct {
	StorageName string     `json:"storageName,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	State       string     `json:"state,omitempty"`
}

type S3 struct {
	Bucket      string `json:"bucket"`
	EndpointURL string `json:"endpointUrl,omitempty"`
	Region      string `json:"region,omitempty"`
}

type Anonymized struct {
	Run        string `json:"run"`
	Policy     string `json:"policy"`
	PolicyHash string `json:"policyHash"`
}

// Store lets controllers exercise publication without an S3 service.
type Store interface {
	PutJSON(context.Context, string, any) (string, error)
	GetJSON(context.Context, string, any) error
	Head(context.Context, string) (objectstore.Metadata, error)
}

// BackupName lets the S3 writer attach publication metadata without importing this package.
func (d Document) BackupName() string { return d.Name }

var destinationPattern = regexp.MustCompile(`^s3://[^/]+/.+`)

func (d Document) Validate() error {
	if d.Name == "" {
		return errors.New("pointer name is required")
	}
	u, err := url.Parse(d.Destination)
	if err != nil || !destinationPattern.MatchString(d.Destination) || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("pointer destination must identify an s3 bucket and object")
	}
	if d.SchemaVersion < 1 {
		return errors.New("pointer schemaVersion must be positive")
	}
	if d.S3 != nil && d.S3.EndpointURL != "" {
		if _, err := objectstore.ParseEndpoint(d.S3.EndpointURL); err != nil {
			return errors.New("pointer S3 endpoint URL is invalid")
		}
	}
	if d.PublicURL != "" {
		if _, err := objectstore.ParseURL(d.PublicURL, true); err != nil {
			return errors.New("pointer public URL is invalid")
		}
	}
	return nil
}

// MarshalJSON always publishes v2, including when a caller passes a decoded v1 document.
func (d Document) MarshalJSON() ([]byte, error) {
	type wireDocument Document
	d.SchemaVersion = SchemaVersion
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(wireDocument(d))
}

func (d *Document) UnmarshalJSON(data []byte) error {
	type wireDocument Document
	decoded := wireDocument{SchemaVersion: 1}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	result := Document(decoded)
	if err := result.Validate(); err != nil {
		return err
	}
	*d = result
	return nil
}

func Encode(document Document) ([]byte, error) { return json.Marshal(document) }

func Decode(data []byte) (*Document, error) {
	var document Document
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	return &document, nil
}
