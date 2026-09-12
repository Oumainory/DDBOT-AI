// Package mediacache implements the bounded, public-media-only cache used by
// replay.  It is best effort and has no goroutine or retry worker; a cache
// failure never changes an already committed RouteDecision.
package mediacache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
)

const (
	MaxFileBytes     int64 = 10 << 20
	MaxEventBytes    int64 = 30 << 20
	MaxGlobalBytes   int64 = 2 << 30
	DefaultRetention       = 7 * 24 * time.Hour
	MaxRedirects           = 3
)

var (
	ErrInvalidMediaURL  = errors.New("mediacache: invalid or unsafe media URL")
	ErrMediaTooLarge    = errors.New("mediacache: media exceeds size limit")
	ErrEventTooLarge    = errors.New("mediacache: event media exceeds size limit")
	ErrUnsupportedMIME  = errors.New("mediacache: unsupported media type")
	ErrCacheUnavailable = errors.New("mediacache: cache unavailable")
)

type Config struct {
	Root           string
	Repository     *platformdb.Phase5Repository
	Client         *http.Client
	Now            func() time.Time
	Retention      time.Duration
	MaxFileBytes   int64
	MaxEventBytes  int64
	MaxGlobalBytes int64
}

type Entry struct {
	ID         string
	SHA256     string
	SizeBytes  int64
	MIMEType   string
	StorageKey string
	Path       string
	ExpiresAt  time.Time
}

type Cache struct {
	root                         string
	repository                   *platformdb.Phase5Repository
	client                       *http.Client
	now                          func() time.Time
	retention                    time.Duration
	maxFile, maxEvent, maxGlobal int64
}

func New(config Config) *Cache {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Retention <= 0 {
		config.Retention = DefaultRetention
	}
	if config.MaxFileBytes <= 0 {
		config.MaxFileBytes = MaxFileBytes
	}
	if config.MaxEventBytes <= 0 {
		config.MaxEventBytes = MaxEventBytes
	}
	if config.MaxGlobalBytes <= 0 {
		config.MaxGlobalBytes = MaxGlobalBytes
	}
	if config.Client == nil {
		config.Client = &http.Client{Timeout: 20 * time.Second}
	}
	return &Cache{root: strings.TrimSpace(config.Root), repository: config.Repository, client: config.Client, now: config.Now, retention: config.Retention, maxFile: config.MaxFileBytes, maxEvent: config.MaxEventBytes, maxGlobal: config.MaxGlobalBytes}
}

func ValidateURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ErrInvalidMediaURL
	}
	host := parsed.Hostname()
	if host == "" {
		return ErrInvalidMediaURL
	}
	// Resolve every request, including redirects, before opening the socket.
	// This rejects loopback, link-local, RFC1918/private and metadata ranges;
	// a hostname resolving to any unsafe address is rejected conservatively.
	ips, err := net.LookupIP(host)
	if err != nil {
		return ErrInvalidMediaURL
	}
	for _, ip := range ips {
		if unsafeIP(ip) {
			return ErrInvalidMediaURL
		}
	}
	return nil
}

func unsafeIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	// IPv4 metadata service and common carrier-grade/private ranges.
	v4 := ip.To4()
	if v4 != nil {
		if v4[0] == 169 && v4[1] == 254 {
			return true
		}
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
	}
	return false
}

// safeDialContext performs the same address policy immediately before the
// socket is opened, closing the DNS-rebinding gap between ValidateURL and the
// HTTP transport's own resolver. A custom dialer is used only after the host
// has passed the check; the selected address is dialled directly.
func safeDialContext(_ func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrInvalidMediaURL
		}
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return nil, ErrInvalidMediaURL
		}
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		for _, ip := range ips {
			if unsafeIP(ip) {
				continue
			}
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			err = dialErr
		}
		if err == nil {
			err = ErrInvalidMediaURL
		}
		return nil, err
	}
}

func (c *Cache) Fetch(ctx context.Context, routeDecisionID, eventID, rawURL string) (Entry, error) {
	if c == nil || c.repository == nil || strings.TrimSpace(c.root) == "" {
		return Entry{}, ErrCacheUnavailable
	}
	if err := ValidateURL(rawURL); err != nil {
		return Entry{}, err
	}
	client := *c.client
	if transport, ok := c.client.Transport.(*http.Transport); ok {
		clone := transport.Clone()
		clone.DialContext = safeDialContext(clone.DialContext)
		client.Transport = clone
	} else if c.client.Transport == nil {
		client.Transport = &http.Transport{DialContext: safeDialContext(nil)}
	} else {
		// An arbitrary RoundTripper can bypass the DNS/IP policy entirely. It is
		// safer to reject it than to claim that the V1 SSRF boundary still holds.
		return Entry{}, ErrCacheUnavailable
	}
	redirects := 0
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		redirects++
		if redirects > MaxRedirects {
			return ErrInvalidMediaURL
		}
		return ValidateURL(req.URL.String())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(rawURL), nil)
	if err != nil {
		return Entry{}, ErrInvalidMediaURL
	}
	resp, err := client.Do(req)
	if err != nil {
		return Entry{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Entry{}, fmt.Errorf("mediacache: remote status %d", resp.StatusCode)
	}
	if resp.ContentLength > c.maxFile {
		return Entry{}, ErrMediaTooLarge
	}
	total, err := c.repository.MediaCacheUsage(ctx)
	if err == nil && total >= c.maxGlobal {
		c.evict(ctx, c.now().Add(-c.retention), total-c.maxGlobal+c.maxFile)
	}
	return c.putStream(ctx, routeDecisionID, eventID, rawURL, resp.Body, resp.Header.Get("Content-Type"))
}

func (c *Cache) Put(ctx context.Context, routeDecisionID, eventID, sourceURL string, data []byte, contentType string) (Entry, error) {
	if c == nil || c.repository == nil || strings.TrimSpace(c.root) == "" {
		return Entry{}, ErrCacheUnavailable
	}
	if int64(len(data)) > c.maxFile {
		return Entry{}, ErrMediaTooLarge
	}
	if int64(len(data)) > c.maxEvent {
		return Entry{}, ErrEventTooLarge
	}
	return c.putStream(ctx, routeDecisionID, eventID, sourceURL, bytes.NewReader(data), contentType)
}

func (c *Cache) putStream(ctx context.Context, routeDecisionID, eventID, sourceURL string, reader io.Reader, contentType string) (Entry, error) {
	if err := os.MkdirAll(c.root, 0o700); err != nil {
		return Entry{}, ErrCacheUnavailable
	}
	tmp, err := os.CreateTemp(c.root, ".media-*")
	if err != nil {
		return Entry{}, ErrCacheUnavailable
	}
	tmpPath := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }()
	hash := sha256.New()
	limited := io.LimitReader(reader, c.maxFile+1)
	n, err := io.Copy(io.MultiWriter(tmp, hash), limited)
	if err != nil {
		return Entry{}, err
	}
	if n > c.maxFile {
		return Entry{}, ErrMediaTooLarge
	}
	if err := tmp.Sync(); err != nil {
		return Entry{}, ErrCacheUnavailable
	}
	if err := tmp.Close(); err != nil {
		return Entry{}, ErrCacheUnavailable
	}
	dataType := strings.TrimSpace(strings.Split(contentType, ";")[0])
	if dataType == "" {
		file, openErr := os.Open(tmpPath)
		if openErr != nil {
			return Entry{}, ErrCacheUnavailable
		}
		var sniff [512]byte
		readN, _ := file.Read(sniff[:])
		_ = file.Close()
		dataType = http.DetectContentType(sniff[:readN])
	}
	if !allowedMIME(dataType) {
		return Entry{}, ErrUnsupportedMIME
	}
	digest := hash.Sum(nil)
	sha := "sha256:" + hex.EncodeToString(digest)
	if eventID != "" {
		if current, usageErr := c.repository.MediaCacheEventUsage(ctx, eventID); usageErr == nil && current+n > c.maxEvent {
			return Entry{}, ErrEventTooLarge
		}
	}
	if existing, e := c.repository.MediaCacheEntryBySHA(ctx, sha); e == nil {
		if value, ok := c.reuseExisting(ctx, existing, routeDecisionID, eventID, sourceURL); ok {
			return value, nil
		}
	}
	if c.maxGlobal > 0 && n > c.maxGlobal {
		return Entry{}, ErrMediaTooLarge
	}
	if usage, usageErr := c.repository.MediaCacheUsage(ctx); usageErr == nil && usage+n > c.maxGlobal {
		c.evict(ctx, c.now().Add(-c.retention), usage+n-c.maxGlobal)
		if after, afterErr := c.repository.MediaCacheUsage(ctx); afterErr == nil && after+n > c.maxGlobal {
			return Entry{}, ErrMediaTooLarge
		}
	}
	key := filepath.ToSlash(filepath.Join("sha256", hex.EncodeToString(digest)))
	finalPath := filepath.Join(c.root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return Entry{}, ErrCacheUnavailable
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		// Another writer may have won the content-addressed race. Reuse its
		// durable entry when the file is present; never overwrite or remove it.
		if existing, lookupErr := c.repository.MediaCacheEntryBySHA(ctx, sha); lookupErr == nil {
			if value, ok := c.reuseExisting(ctx, existing, routeDecisionID, eventID, sourceURL); ok {
				return value, nil
			}
		}
		return Entry{}, ErrCacheUnavailable
	}
	stamp := c.now().UTC()
	id := "media_" + hex.EncodeToString(digest[:8])
	record := platformdb.MediaCacheEntryRecord{ID: id, SHA256: sha, SizeBytes: n, MIMEType: dataType, StorageKey: key, CreatedAt: stamp, ExpiresAt: stamp.Add(c.retention), LastAccessedAt: stamp}
	if err := c.repository.AddMediaCacheEntry(ctx, record); err != nil {
		// Do not leave a file that has no metadata owner. If a concurrent writer
		// inserted the same digest, keep the winner's file and reuse it.
		if existing, lookupErr := c.repository.MediaCacheEntryBySHA(ctx, sha); lookupErr == nil {
			if value, ok := c.reuseExisting(ctx, existing, routeDecisionID, eventID, sourceURL); ok {
				return value, nil
			}
		}
		_ = os.Remove(finalPath)
		return Entry{}, ErrCacheUnavailable
	}
	_ = c.repository.LinkMediaCache(ctx, platformdb.MediaCacheLinkRecord{EntryID: id, RouteDecisionID: routeDecisionID, EventID: eventID, SourceURL: sourceURL, CreatedAt: stamp})
	if usage, e := c.repository.MediaCacheUsage(ctx); e == nil && usage > c.maxGlobal {
		c.evict(ctx, stamp.Add(-c.retention), usage-c.maxGlobal)
	}
	return Entry{ID: id, SHA256: sha, SizeBytes: n, MIMEType: dataType, StorageKey: key, Path: finalPath, ExpiresAt: stamp.Add(c.retention)}, nil
}

// reuseExisting returns a content-addressed entry only while it is still
// within its retention window and its file remains inside the configured cache
// root. Expired metadata is removed before a same-content fetch can be
// accepted; otherwise the UNIQUE sha row would accidentally resurrect a
// seven-day-old object forever.
func (c *Cache) reuseExisting(ctx context.Context, existing platformdb.MediaCacheEntryRecord, routeDecisionID, eventID, sourceURL string) (Entry, bool) {
	now := c.now().UTC()
	path, pathErr := c.safeStoragePath(existing.StorageKey)
	if !existing.ExpiresAt.After(now) {
		if pathErr == nil {
			_ = os.Remove(path)
		}
		_ = c.repository.DeleteMediaCacheEntry(ctx, existing.ID)
		return Entry{}, false
	}
	if pathErr != nil {
		return Entry{}, false
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return Entry{}, false
	}
	_ = c.repository.TouchMediaCacheEntry(ctx, existing.ID, now)
	_ = c.repository.LinkMediaCache(ctx, platformdb.MediaCacheLinkRecord{EntryID: existing.ID, RouteDecisionID: routeDecisionID, EventID: eventID, SourceURL: sourceURL, CreatedAt: now})
	return Entry{ID: existing.ID, SHA256: existing.SHA256, SizeBytes: existing.SizeBytes, MIMEType: existing.MIMEType, StorageKey: existing.StorageKey, Path: path, ExpiresAt: existing.ExpiresAt}, true
}

func allowedMIME(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if med, _, err := mime.ParseMediaType(value); err == nil {
		value = med
	}
	switch value {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/avif", "application/pdf":
		return true
	}
	// SVG and other active image formats are intentionally excluded. The
	// replay renderer only needs static raster media and PDF attachments.
	return false
}

func (c *Cache) Read(entry Entry) ([]byte, error) {
	if c == nil || strings.TrimSpace(entry.Path) == "" || !filepath.IsAbs(entry.Path) {
		return nil, ErrCacheUnavailable
	}
	root, err := filepath.Abs(c.root)
	if err != nil {
		return nil, ErrCacheUnavailable
	}
	target, err := filepath.Abs(entry.Path)
	if err != nil {
		return nil, ErrCacheUnavailable
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, ErrCacheUnavailable
	}
	// Lexical containment is not enough when an operator or a compromised
	// process places a symlink below the cache root. Resolve both paths before
	// opening bytes so a cache entry can never escape its configured directory.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, ErrCacheUnavailable
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return nil, ErrCacheUnavailable
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
		return nil, ErrCacheUnavailable
	}
	return os.ReadFile(resolvedTarget)
}

// ReadReference returns bytes for a still-live public URL linked to an event.
// It is the cache-first half of replay fallback and never performs a source
// API request. A missing, expired or tampered entry is treated as a cache miss
// so the caller can try the original public URL next.
func (c *Cache) ReadReference(ctx context.Context, eventID, sourceURL string) (Entry, []byte, error) {
	if c == nil || c.repository == nil {
		return Entry{}, nil, ErrCacheUnavailable
	}
	record, err := c.repository.MediaCacheEntryForSourceURL(ctx, eventID, sourceURL)
	if err != nil {
		return Entry{}, nil, err
	}
	if !record.ExpiresAt.After(c.now().UTC()) {
		return Entry{}, nil, ErrCacheUnavailable
	}
	path, err := c.safeStoragePath(record.StorageKey)
	if err != nil {
		return Entry{}, nil, err
	}
	entry := Entry{ID: record.ID, SHA256: record.SHA256, SizeBytes: record.SizeBytes, MIMEType: record.MIMEType, StorageKey: record.StorageKey, Path: path, ExpiresAt: record.ExpiresAt}
	data, err := c.Read(entry)
	if err != nil {
		return Entry{}, nil, err
	}
	_ = c.repository.TouchMediaCacheEntry(ctx, record.ID, c.now().UTC())
	return entry, data, nil
}

func sameFilePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs))
}

// evict removes database metadata and then unlinks only files returned by the
// repository. A stale/missing file is harmless; cache eviction remains best
// effort and never affects a committed route decision.
func (c *Cache) evict(ctx context.Context, before time.Time, bytesOver int64) {
	entries, err := c.repository.EvictMediaCacheEntries(ctx, before, bytesOver)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if path, pathErr := c.safeStoragePath(entry.StorageKey); pathErr == nil {
			_ = os.Remove(path)
		}
	}
}

func (c *Cache) safeStoragePath(storageKey string) (string, error) {
	if c == nil || strings.TrimSpace(c.root) == "" || strings.TrimSpace(storageKey) == "" || filepath.IsAbs(storageKey) {
		return "", ErrCacheUnavailable
	}
	root, err := filepath.Abs(c.root)
	if err != nil {
		return "", ErrCacheUnavailable
	}
	path, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(storageKey)))
	if err != nil {
		return "", ErrCacheUnavailable
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrCacheUnavailable
	}
	return path, nil
}
