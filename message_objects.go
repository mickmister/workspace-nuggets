package nuggets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var errMessageCAS = errors.New("nug object generation changed")

// Legacy fields remain readable in private registries so upgrading cannot break
// unrelated document/Beads operations. All message/media operations enforce
// validateWrite before opening a transport; Git and old pilot layouts are denied.
type MessageTarget struct {
	Payload         string `json:"payload"`
	Catalog         string `json:"catalog"`
	Remote          string `json:"remote,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	Bucket          string `json:"bucket,omitempty"`
	Prefix          string `json:"prefix,omitempty"`
	Region          string `json:"region,omitempty"`
	CredentialsFile string `json:"credentials_file,omitempty"`
	Checkpoint      string `json:"checkpoint,omitempty"`
	Layout          string `json:"layout,omitempty"`
}

func (p MessageTarget) validate() error {
	if p.Payload != "git" && p.Payload != "s3" || p.Catalog != "git" && p.Catalog != "s3" {
		return invalid("invalid nug target backend")
	}
	if p.Payload == "s3" || p.Catalog == "s3" {
		u, err := url.Parse(p.Endpoint)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Scheme != "http" && u.Scheme != "https" {
			return invalid("S3 endpoint must be an http(s) origin without credentials or query")
		}
		if p.Bucket == "" || p.Prefix != "" && validateName(strings.TrimSuffix(p.Prefix, "/")) != nil {
			return invalid("S3 target requires a bucket and safe prefix")
		}
	}
	return nil
}
func (p MessageTarget) validateWrite() error {
	if p.Payload != "s3" || p.Catalog != "s3" || p.Checkpoint == "git" {
		return invalid("messages and media require S3 payloads/catalogs and S3 or local checkpoints; Git writes are disabled")
	}
	if p.Layout != "workspace-v2" {
		return invalid("legacy pilot layout is disabled; explicitly bind a new workspace-v2 target, without retargeting existing data")
	}
	if p.Checkpoint != "" && p.Checkpoint != "local" && p.Checkpoint != "s3" && p.Checkpoint != "catalog" {
		return invalid("checkpoint must be s3, catalog or local")
	}
	return p.validate()
}

type messageObjects interface {
	get(context.Context, string) ([]byte, string, error)
	cas(context.Context, string, string, []byte, map[string][]byte) (string, error)
}

func immutableObject(ctx context.Context, objects messageObjects, key string, data []byte) error {
	if len(data) > nugObjectLimit {
		return invalid("nug object exceeds 4 MiB transport limit")
	}
	_, err := objects.cas(ctx, key, "", data, nil)
	if errors.Is(err, errMessageCAS) {
		actual, _, readErr := objects.get(ctx, key)
		if readErr != nil {
			return readErr
		}
		if bytes.Equal(actual, data) {
			return nil
		}
		return &OperationError{Code: "corrupt_content", Message: "immutable nug object has conflicting bytes"}
	}
	if err != nil {
		return err
	}
	actual, version, err := objects.get(ctx, key)
	if err != nil {
		return err
	}
	if version == "" || !bytes.Equal(actual, data) {
		return &OperationError{Code: "corrupt_content", Message: "immutable nug object failed readback verification"}
	}
	return nil
}

type messageS3Objects struct {
	client         *minio.Client
	bucket, prefix string
}

func newNugS3(p MessageTarget, workspace, kind string) (messageS3Objects, error) {
	if err := p.validateWrite(); err != nil {
		return messageS3Objects{}, err
	}
	u, _ := url.Parse(p.Endpoint)
	creds := credentials.NewEnvAWS()
	if p.CredentialsFile != "" {
		data, err := os.ReadFile(p.CredentialsFile)
		if err != nil {
			return messageS3Objects{}, err
		}
		values := map[string]string{}
		for _, line := range strings.Split(string(data), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if ok {
				values[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		access, secret := values["AWS_ACCESS_KEY_ID"], values["AWS_SECRET_ACCESS_KEY"]
		if access == "" {
			access, secret = values["MINIO_ROOT_USER"], values["MINIO_ROOT_PASSWORD"]
		}
		if access == "" || secret == "" {
			return messageS3Objects{}, invalid("credential file requires AWS or MinIO credential variable names")
		}
		creds = credentials.NewStaticV4(access, secret, values["AWS_SESSION_TOKEN"])
	}
	c, err := minio.New(u.Host, &minio.Options{Creds: creds, Secure: u.Scheme == "https", Region: p.Region, BucketLookup: minio.BucketLookupPath})
	return messageS3Objects{c, p.Bucket, path.Join(p.Prefix, "workspaces", workspace, kind) + "/"}, err
}
func (s messageS3Objects) get(ctx context.Context, key string) ([]byte, string, error) {
	if err := validateName(key); err != nil {
		return nil, "", err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, s.prefix+key, minio.GetObjectOptions{})
	if err != nil {
		return nil, "", err
	}
	defer obj.Close()
	stat, err := obj.Stat()
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, "", nil
		}
		return nil, "", err
	}
	if stat.Size > nugObjectLimit {
		return nil, "", invalid("nug object exceeds 4 MiB transport limit")
	}
	data, err := io.ReadAll(io.LimitReader(obj, nugObjectLimit+1))
	if len(data) > nugObjectLimit {
		return nil, "", invalid("nug object exceeds 4 MiB transport limit")
	}
	return data, stat.ETag, err
}
func (s messageS3Objects) cas(ctx context.Context, key, expected string, data []byte, extras map[string][]byte) (string, error) {
	if err := validateName(key); err != nil {
		return "", err
	}
	if len(data) > nugObjectLimit {
		return "", invalid("nug object exceeds 4 MiB transport limit")
	}
	for name, content := range extras {
		if err := immutableObject(ctx, s, name, content); err != nil {
			return "", err
		}
	}
	contentType := "application/json"
	if strings.HasSuffix(key, ".gz") {
		contentType = "application/gzip"
	}
	opts := minio.PutObjectOptions{ContentType: contentType, DisableMultipart: true}
	if expected == "" {
		opts.SetMatchETagExcept("*")
	} else {
		opts.SetMatchETag(expected)
	}
	info, err := s.client.PutObject(ctx, s.bucket, s.prefix+key, bytes.NewReader(data), int64(len(data)), opts)
	if err != nil {
		if minio.ToErrorResponse(err).StatusCode == 412 {
			return "", errMessageCAS
		}
		found, version, readErr := s.get(ctx, key)
		if readErr == nil && bytes.Equal(found, data) {
			return version, nil
		}
	}
	return info.ETag, err
}
func encodeMessageJSON(v any) []byte { data, _ := json.Marshal(v); return data }
