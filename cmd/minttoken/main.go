// Command minttoken prints local-HMAC identity tokens for development and
// smoke deployments (spec 0003: the scheme itself is dev/test only). In
// production, executor identities are projected ServiceAccount tokens
// verified via TokenReview; the controller uses an observed:write token
// minted with the control plane's key.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shitamachi/forgelet/internal/run/model"
	"github.com/shitamachi/forgelet/internal/security/identity"
)

func main() {
	var (
		keyHex    = flag.String("key", "", "hex token key (must match -token-key of the server)")
		namespace = flag.String("namespace", "forgelet-system", "identity namespace")
		podUID    = flag.String("pod-uid", "dev", "bound pod uid")
		jobRunID  = flag.String("jobrun", "", "bound job run id (empty + observed:write for controllers)")
		scopes    = flag.String("scopes", identity.ScopeObservedWrite, "comma-separated scopes")
		ttl       = flag.Duration("ttl", time.Hour, "token lifetime (capped at 1h)")
	)
	flag.Parse()
	if *keyHex == "" {
		fmt.Fprintln(os.Stderr, "minttoken: -key is required")
		os.Exit(2)
	}
	key, err := hex.DecodeString(*keyHex)
	if err != nil || len(key) < 32 {
		fmt.Fprintln(os.Stderr, "minttoken: -key must be at least 32 bytes of hex")
		os.Exit(2)
	}
	now := time.Now()
	expires := now.Add(*ttl)
	if expires.After(now.Add(identity.MaxTTL)) {
		expires = now.Add(identity.MaxTTL)
	}
	// LocalIssuer requires all three bindings; controller tokens carry the
	// literal "controller" — it can never collide with a ULID JobRunID.
	if *jobRunID == "" {
		*jobRunID = "controller"
	}
	token, err := identity.NewLocalIssuer(key, func() time.Time { return now }).Issue(context.Background(), identity.Identity{
		Audience:  identity.Audience,
		Namespace: *namespace,
		PodUID:    *podUID,
		JobRunID:  model.JobRunID(*jobRunID),
		Scopes:    strings.Split(*scopes, ","),
		IssuedAt:  now,
		ExpiresAt: expires,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "minttoken: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(token)
}
