package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/gateway"
	"github.com/theatarr/theatarr/internal/plex"
	"github.com/theatarr/theatarr/internal/plexauth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/web"
)

// newPlexLogin builds Plex sign-in (docs/design/users.md) when the policy has metadata.sources.plex-tv and library.plex
// with its token set; otherwise nil, and the login page offers the local form only. plex.tv and the local server's
// /identity are both reached through the gateway.
func newPlexLogin(pol *policy.Policy, au *auth.Service, gw *gateway.Gateway, log *slog.Logger) (web.PlexLogin, error) {
	if pol.Metadata == nil || pol.Metadata.Sources[policy.PlexTVSource] == nil {
		return nil, nil
	}
	src := pol.Metadata.Sources[policy.PlexTVSource]
	p := pol.LibrarySettings().Plex
	if p == nil {
		log.Warn("plex sign-in off: metadata.sources.plex-tv is set but library.plex is not")
		return nil, nil
	}
	tok := pol.Getenv(p.TokenEnv)
	if tok == "" {
		log.Warn("plex sign-in off: library.plex's token is not set", "env", p.TokenEnv)
		return nil, nil
	}
	server, err := plex.New(gw, p.URL, tok)
	if err != nil {
		return nil, err
	}
	svc, err := plexauth.New(plexauth.Config{Doer: gw, BaseURL: src.URL, Product: "Theatarr", ServerID: server.Identity,
		ClientID: func(ctx context.Context) (string, error) { return plexClientID(ctx, au) }})
	if err != nil {
		return nil, err
	}
	log.Info("plex sign-in on", "plex_tv", src.URL)
	return svc, nil
}

// plexClientID is this install's X-Plex-Client-Identifier, made once and kept in web_setting.
func plexClientID(ctx context.Context, au *auth.Service) (string, error) {
	id, err := au.Setting(ctx, auth.SettingPlexClientID, "")
	if err != nil || id != "" {
		return id, err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id = "theatarr-" + hex.EncodeToString(b)
	return id, au.SetSetting(ctx, auth.SettingPlexClientID, id)
}
