
package main

import (
  "fmt"
  "time"
  "strings"
  "net/url"
  "net/http"

  "github.com/namsral/flag"
  "github.com/op/go-logging"
)

// Vars
var fw *ForwardAuth;
var log = logging.MustGetLogger("traefik-forward-auth")

// Primary handler
func handler(w http.ResponseWriter, r *http.Request) {
  // Parse uri
  uri, err := url.Parse(r.Header.Get("X-Forwarded-Uri"))
  if err != nil {
    log.Error("Error parsing url")
    http.Error(w, "Service unavailable", 503)
    return
  }

  // Handle callback
  if uri.Path == fw.Path {
    handleCallback(w, r, uri.Query())
    return
  }

  // Get auth cookie
  c, err := r.Cookie(fw.CookieName)
  if err != nil {
    // Error indicates no cookie, generate nonce
    err, nonce := fw.Nonce()
    if err != nil {
      log.Error("Error generating nonce")
      http.Error(w, "Service unavailable", 503)
      return
    }

    // Set the CSRF cookie
    http.SetCookie(w, fw.MakeCSRFCookie(r, nonce))
    log.Debug("Set CSRF cookie and redirecting to google login")

    // Forward them on
    http.Redirect(w, r, fw.GetLoginURL(r, nonce), http.StatusTemporaryRedirect)

    return
  }

  // Validate cookie
  valid, email, err := fw.ValidateCookie(r, c)
  if !valid {
    log.Debugf("Invlaid cookie: %s", err)
    http.Error(w, "Not authorized", 401)
    return
  }

  // Validate user
  valid = fw.ValidateEmail(email)
  if !valid {
    log.Debugf("Invalid email: %s", email)
    http.Error(w, "Not authorized", 401)
    return
  }

  // Valid request
  w.WriteHeader(200)
}


// Authenticate user after they have come back from google
func handleCallback(w http.ResponseWriter, r *http.Request, qs url.Values) {
  // Check for CSRF cookie
  csrfCookie, err := r.Cookie(fw.CSRFCookieName)
  if err != nil {
    log.Debug("Missing csrf cookie")
    http.Error(w, "Not authorized", 401)
    return
  }

  // Validate state
  state := qs.Get("state")
  valid, redirect, err := fw.ValidateCSRFCookie(csrfCookie, state)
  if !valid {
    log.Debugf("Invalid oauth state, expected '%s', got '%s'\n", csrfCookie.Value, state)
    http.Error(w, "Not authorized", 401)
    return
  }

  // Clear CSRF cookie
  http.SetCookie(w, fw.ClearCSRFCookie(r))

  // Exchange code for token
  token, err := fw.ExchangeCode(r, qs.Get("code"))
  if err != nil {
    log.Debugf("Code exchange failed with: %s\n", err)
    http.Error(w, "Service unavailable", 503)
    return
  }

  // Get user
  user, err := fw.GetUser(token)
  if err != nil {
    log.Debugf("Error getting user: %s\n", err)
    return
  }

  // Generate cookie
  http.SetCookie(w, fw.MakeCookie(r, user.Email))
  log.Debugf("Generated auth cookie for %s\n", user.Email)

  // Redirect
  http.Redirect(w, r, redirect, http.StatusTemporaryRedirect)
}


// Main
func main() {
  // Parse options
  flag.String(flag.DefaultConfigFlagname, "", "Path to config file")
  path := flag.String("url-path", "_oauth", "Callback URL")
  lifetime := flag.Int("lifetime", 43200, "Session length in seconds")
  secret := flag.String("secret", "", "*Secret used for signing (required)")
  authHost := flag.String("auth-host", "", "Central auth login")
  clientId := flag.String("client-id", "", "*Google Client ID (required)")
  clientSecret := flag.String("client-secret", "", "*Google Client Secret (required)")
  cookieName := flag.String("cookie-name", "_forward_auth", "Cookie Name")
  cSRFCookieName := flag.String("csrf-cookie-name", "_forward_auth_csrf", "CSRF Cookie Name")
  cookieDomainList := flag.String("cookie-domains", "", "Comma separated list of cookie domains") //todo
  cookieSecret := flag.String("cookie-secret", "", "depreciated")
  cookieSecure := flag.Bool("cookie-secure", true, "Use secure cookies")
  domainList := flag.String("domain", "", "Comma separated list of email domains to allow")
  emailWhitelist := flag.String("whitelist", "", "Comma separated list of emails to allow")
  prompt := flag.String("prompt", "", "Space separated list of OpenID prompt options")

  scope := flag.String("scope", "", "OAuth2 Scope URL(s)")
  loginUrlStr := flag.String("login-url", "", "OAuth2 Login URL")
  tokenUrlStr := flag.String("token-url", "", "OAuth2 Token URL")
  userUrlStr := flag.String("user-url", "", "OAuth2 Validate/User URL")

  flag.Parse()

  // Backwards compatability
  if *secret == "" && *cookieSecret != "" {
    *secret = *cookieSecret
  }

  // Check for show stopper errors
  stop := false
  if *clientId == "" {
    stop = true
    log.Critical("client-id must be set")
  }
  if *clientSecret == "" {
    stop = true
    log.Critical("client-secret must be set")
  }
  if *secret == "" {
    stop = true
    log.Critical("secret must be set")
  }
  loginUrl, err := url.Parse(*loginUrlStr)
  if err != nil {
    stop = true
    log.Critical("invalid OAuth2 Login URL")
  }
  tokenUrl, err := url.Parse(*tokenUrlStr)
  if err != nil {
    stop = true
    log.Critical("invalid OAuth2 Token URL")
  }
  userUrl, err := url.Parse(*userUrlStr)
  if err != nil {
    stop = true
    log.Critical("invalid OAuth2 Validate/User URL")
  }
  if stop {
    return
  }

  // Parse lists
  var cookieDomains []CookieDomain
  if *cookieDomainList != "" {
    for _, d := range strings.Split(*cookieDomainList, ",") {
      cookieDomain := NewCookieDomain(d)
      cookieDomains = append(cookieDomains, *cookieDomain)
    }
  }

  var domain []string
  if *domainList != "" {
    domain = strings.Split(*domainList, ",")
  }
  var whitelist []string
  if *emailWhitelist != "" {
    whitelist = strings.Split(*emailWhitelist, ",")
  }

  // Setup
  fw = &ForwardAuth{
    Path: fmt.Sprintf("/%s", *path),
    Lifetime: time.Second * time.Duration(*lifetime),
    Secret: []byte(*secret),
    AuthHost: *authHost,

    ClientId: *clientId,
    ClientSecret: *clientSecret,

    Scope: *scope,
    LoginURL: loginUrl,
    TokenURL: tokenUrl,
    UserURL: userUrl,

    CookieName: *cookieName,
    CSRFCookieName: *cSRFCookieName,
    CookieDomains: cookieDomains,
    CookieSecure: *cookieSecure,

    Domain: domain,
    Whitelist: whitelist,

    Prompt: *prompt,
  }

  // Attach handler
  http.HandleFunc("/", handler)

  log.Debugf("Staring with options: %#v", fw)
  log.Notice("Litening on :4181")
  log.Notice(http.ListenAndServe(":4181", nil))
}
