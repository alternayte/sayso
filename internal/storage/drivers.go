package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config is the config of an s3 store (§15.2). The secret is the secret
// access key; without an access key the default credential chain applies.
type S3Config struct {
	Endpoint  string `json:"endpoint" doc:"Host and port, for example s3.eu-west-1.amazonaws.com or minio:9000"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket"`
	PathStyle bool   `json:"path_style,omitempty"`
	Insecure  bool   `json:"insecure,omitempty" doc:"Use http instead of https"`
	AccessKey string `json:"access_key,omitempty"`
	Prefix    string `json:"prefix,omitempty" doc:"Key prefix inside the bucket"`
}

// AzConfig is the config of an azblob store. The secret is the account key
// or, with auth connection_string, the connection string; auth default uses
// the default Azure credential.
type AzConfig struct {
	AccountURL string `json:"account_url,omitempty" doc:"For example https://acme.blob.core.windows.net"`
	Container  string `json:"container"`
	Auth       string `json:"auth" enum:"key,connection_string,default"`
	Prefix     string `json:"prefix,omitempty"`
}

// S3 keeps objects in an S3-compatible bucket.
type S3 struct {
	c      *minio.Client
	bucket string
	prefix string
}

// NewS3 opens an s3 store.
func NewS3(cfg S3Config, secret string) (*S3, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("s3: endpoint and bucket are required")
	}
	var creds *credentials.Credentials
	if cfg.AccessKey != "" {
		creds = credentials.NewStaticV4(cfg.AccessKey, secret, "")
	} else {
		creds = credentials.NewChainCredentials([]credentials.Provider{&credentials.EnvAWS{}, &credentials.FileAWSCredentials{}, &credentials.IAM{}})
	}
	lookup := minio.BucketLookupAuto
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	c, err := minio.New(cfg.Endpoint, &minio.Options{Creds: creds, Secure: !cfg.Insecure, Region: cfg.Region, BucketLookup: lookup})
	if err != nil {
		return nil, err
	}
	return &S3{c: c, bucket: cfg.Bucket, prefix: strings.Trim(cfg.Prefix, "/")}, nil
}

func (s *S3) key(k string) string {
	if s.prefix == "" {
		return k
	}
	return s.prefix + "/" + k
}

func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	_, err := s.c.PutObject(ctx, s.bucket, s.key(key), r, size, minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return err
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	o, err := s.c.GetObject(ctx, s.bucket, s.key(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, s3err(err)
	}
	// GetObject is lazy; Stat finds a missing key now.
	if _, err := o.Stat(); err != nil {
		_ = o.Close()
		return nil, s3err(err)
	}
	return o, nil
}

func (s *S3) Stat(ctx context.Context, key string) (Info, error) {
	st, err := s.c.StatObject(ctx, s.bucket, s.key(key), minio.StatObjectOptions{})
	if err != nil {
		return Info{}, s3err(err)
	}
	return Info{Size: st.Size, ModTime: st.LastModified}, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	return s.c.RemoveObject(ctx, s.bucket, s.key(key), minio.RemoveObjectOptions{})
}

func (s *S3) List(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	for o := range s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.key(prefix), Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		k := o.Key
		if s.prefix != "" {
			k = strings.TrimPrefix(k, s.prefix+"/")
		}
		out = append(out, k)
	}
	return out, nil
}

func s3err(err error) error {
	if minio.ToErrorResponse(err).Code == "NoSuchKey" {
		return ErrNotFound
	}
	return err
}

// AzBlob keeps objects in an Azure Blob Storage container.
type AzBlob struct {
	c         *azblob.Client
	container string
	prefix    string
}

// NewAzBlob opens an azblob store.
func NewAzBlob(cfg AzConfig, secret string) (*AzBlob, error) {
	if cfg.Container == "" {
		return nil, errors.New("azblob: container is required")
	}
	var c *azblob.Client
	var err error
	switch cfg.Auth {
	case "connection_string":
		c, err = azblob.NewClientFromConnectionString(secret, nil)
	case "default":
		cred, cerr := azidentity.NewDefaultAzureCredential(nil)
		if cerr != nil {
			return nil, cerr
		}
		c, err = azblob.NewClient(cfg.AccountURL, cred, nil)
	default:
		u, perr := url.Parse(cfg.AccountURL)
		if perr != nil || u.Host == "" {
			return nil, errors.New("azblob: account_url is required for key auth")
		}
		account := strings.SplitN(u.Host, ".", 2)[0]
		if strings.Trim(u.Path, "/") != "" {
			// Emulators put the account in the path: http://host:10000/devstoreaccount1
			account = strings.Trim(u.Path, "/")
		}
		cred, cerr := azblob.NewSharedKeyCredential(account, secret)
		if cerr != nil {
			return nil, cerr
		}
		c, err = azblob.NewClientWithSharedKeyCredential(cfg.AccountURL, cred, nil)
	}
	if err != nil {
		return nil, err
	}
	return &AzBlob{c: c, container: cfg.Container, prefix: strings.Trim(cfg.Prefix, "/")}, nil
}

func (a *AzBlob) key(k string) string {
	if a.prefix == "" {
		return k
	}
	return a.prefix + "/" + k
}

func (a *AzBlob) Put(ctx context.Context, key string, r io.Reader, _ int64) error {
	_, err := a.c.UploadStream(ctx, a.container, a.key(key), r, nil)
	return err
}

func (a *AzBlob) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := a.c.DownloadStream(ctx, a.container, a.key(key), nil)
	if err != nil {
		return nil, azerr(err)
	}
	return resp.Body, nil
}

func (a *AzBlob) Stat(ctx context.Context, key string) (Info, error) {
	p, err := a.c.ServiceClient().NewContainerClient(a.container).NewBlobClient(a.key(key)).GetProperties(ctx, nil)
	if err != nil {
		return Info{}, azerr(err)
	}
	info := Info{}
	if p.ContentLength != nil {
		info.Size = *p.ContentLength
	}
	if p.LastModified != nil {
		info.ModTime = *p.LastModified
	}
	return info, nil
}

func (a *AzBlob) Delete(ctx context.Context, key string) error {
	_, err := a.c.DeleteBlob(ctx, a.container, a.key(key), nil)
	if err := azerr(err); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

func (a *AzBlob) List(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	pager := a.c.NewListBlobsFlatPager(a.container, &azblob.ListBlobsFlatOptions{Prefix: to.Ptr(a.key(prefix))})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, b := range page.Segment.BlobItems {
			k := *b.Name
			if a.prefix != "" {
				k = strings.TrimPrefix(k, a.prefix+"/")
			}
			out = append(out, k)
		}
	}
	return out, nil
}

func azerr(err error) error {
	if err == nil {
		return nil
	}
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return ErrNotFound
	}
	return err
}

// openDriver opens a store from its row and secret.
func openDriver(driver string, config []byte, secret string) (Store, error) {
	switch driver {
	case "s3":
		var c S3Config
		if err := json.Unmarshal(config, &c); err != nil {
			return nil, fmt.Errorf("s3 config: %w", err)
		}
		return NewS3(c, secret)
	case "azblob":
		var c AzConfig
		if err := json.Unmarshal(config, &c); err != nil {
			return nil, fmt.Errorf("azblob config: %w", err)
		}
		return NewAzBlob(c, secret)
	}
	return nil, errors.New("store driver " + driver + " is not available")
}
