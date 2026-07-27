package ingest

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

var ErrTooManyRedirects = errors.New("ingest: too many redirects")

const (
	DefaultMaxRedirects = 5
	DefaultFetchTimeout = 5 * time.Minute
)

type ClientOptions struct {
	MaxRedirects                  int
	Timeout                       time.Duration
	AllowPrivateAddressesForTests bool
}

func NewGuardedClient(opts ClientOptions) *http.Client {
	maxRedirects := opts.MaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = DefaultMaxRedirects
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultFetchTimeout
	}
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if opts.AllowPrivateAddressesForTests {
				return nil
			}
			return CheckDialAddress(address)
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: time.Minute,
			ExpectContinueTimeout: 5 * time.Second,
			MaxIdleConns:          8,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("%w: stopped after %d hops", ErrTooManyRedirects, len(via))
			}
			scheme := strings.ToLower(req.URL.Scheme)
			if scheme != "http" && scheme != "https" {
				return fmt.Errorf("%w: redirect to scheme %q", ErrUnsupportedSource, scheme)
			}
			if !opts.AllowPrivateAddressesForTests {
				if addr, err := netip.ParseAddr(req.URL.Hostname()); err == nil && IsBlockedAddr(addr) {
					return fmt.Errorf("%w: redirect to %s", ErrBlockedAddress, addr)
				}
			}
			return nil
		},
	}
}
