// Copyright © 2017 Heptio
// Copyright © 2017 Craig Tracey
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/heptiolabs/gangway/internal/config"
	"github.com/heptiolabs/gangway/internal/oidc"
	"github.com/heptiolabs/gangway/internal/session"
	"github.com/justinas/alice"
	log "github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
)

var cfg *config.Config
var oauth2Cfg *oauth2.Config
var o2token oidc.OAuth2Token
var gangwayUserSession *session.Session
var transportConfig *config.TransportConfig

// wrapper function for http logging
func httpLogger(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer log.Printf("%s %s %s", r.Method, r.URL, r.RemoteAddr)
		fn(w, r)
	}
}

func main() {
	cfgFile := flag.String("config", "", "The config file to use.")
	flag.Parse()

	var err error
	cfg, err = config.NewConfig(*cfgFile)
	if err != nil {
		log.Errorf("Could not parse config file: %s", err)
		os.Exit(1)
	}

	oauth2Cfg = &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Scopes:       cfg.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:  cfg.AuthorizeURL,
			TokenURL: cfg.TokenURL,
		},
	}

	o2token = &oidc.Token{
		OAuth2Cfg: oauth2Cfg,
	}

	transportConfig = config.NewTransportConfig(cfg.TrustedCAPath)
	gangwayUserSession = session.New(cfg.SessionSecurityKey)

	loginRequiredHandlers := alice.New(loginRequired)

	http.HandleFunc(cfg.GetRootPathPrefix(), httpLogger(homeHandler))
	http.HandleFunc(fmt.Sprintf("%s/login", cfg.HTTPPath), httpLogger(loginHandler))
	http.HandleFunc(fmt.Sprintf("%s/callback", cfg.HTTPPath), httpLogger(callbackHandler))

	// middleware'd routes
	http.Handle(fmt.Sprintf("%s/logout", cfg.HTTPPath), loginRequiredHandlers.ThenFunc(logoutHandler))
	http.Handle(fmt.Sprintf("%s/commandline", cfg.HTTPPath), loginRequiredHandlers.ThenFunc(commandlineHandler))
	http.Handle(fmt.Sprintf("%s/kubeconf", cfg.HTTPPath), loginRequiredHandlers.ThenFunc(kubeConfigHandler))

	// Static assets
	assetUrl := fmt.Sprintf("%s/assets/", cfg.HTTPPath)
	http.Handle(assetUrl, http.StripPrefix(assetUrl, http.FileServer(http.Dir("/assets"))))

	bindAddr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	// create http server with timeouts
	httpServer := &http.Server{
		Addr:         bindAddr,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// start up the http server
	go func() {
		log.Infof("Gangway started! Listening on: %s", bindAddr)

		// exit with FATAL logging why we could not start
		// example: FATA[0000] listen tcp 0.0.0.0:8080: bind: address already in use
		if cfg.ServeTLS {
			log.Fatal(httpServer.ListenAndServeTLS(cfg.CertFile, cfg.KeyFile))
		} else {
			log.Fatal(httpServer.ListenAndServe())
		}
	}()

	// create channel listening for signals so we can have graceful shutdowns
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	<-signalChan

	log.Println("Shutdown signal received, exiting.")
	// close the HTTP server
	httpServer.Shutdown(context.Background())
}
