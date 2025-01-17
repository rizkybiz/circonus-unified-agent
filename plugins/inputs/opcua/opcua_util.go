package opcuaclient

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/debug"
	"github.com/gopcua/opcua/ua"
	"github.com/pkg/errors"
)

// SELF SIGNED CERT FUNCTIONS

func newTempDir() (string, error) {
	dir, err := os.MkdirTemp("", "ssc")
	return dir, fmt.Errorf("temp dir: %w", err)
}

func generateCert(host string, rsaBits int, certFile, keyFile string, dur time.Duration) (string, string) {

	dir, _ := newTempDir()

	if len(host) == 0 {
		log.Fatalf("Missing required host parameter")
	}
	if rsaBits == 0 {
		rsaBits = 2048
	}
	if len(certFile) == 0 {
		certFile = fmt.Sprintf("%s/cert.pem", dir)
	}
	if len(keyFile) == 0 {
		keyFile = fmt.Sprintf("%s/key.pem", dir)
	}

	priv, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		log.Fatalf("failed to generate private key: %s", err)
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(dur)

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		log.Fatalf("failed to generate serial number: %s", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Circonus OPC UA client"},
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		KeyUsage:              x509.KeyUsageContentCommitment | x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageDataEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	hosts := strings.Split(host, ",")
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
		if uri, err := url.Parse(h); err == nil {
			template.URIs = append(template.URIs, uri)
		}
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKey(priv), priv)
	if err != nil {
		log.Fatalf("Failed to create certificate: %s", err)
	}

	certOut, err := os.Create(certFile)
	if err != nil {
		log.Fatalf("failed to open %s for writing: %s", certFile, err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		log.Fatalf("failed to write data to %s: %s", certFile, err)
	}
	if err := certOut.Close(); err != nil {
		log.Fatalf("error closing %s: %s", certFile, err)
	}

	keyOut, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Printf("failed to open %s for writing: %s", keyFile, err)
		return "", ""
	}
	if err := pem.Encode(keyOut, pemBlockForKey(priv)); err != nil {
		log.Fatalf("failed to write data to %s: %s", keyFile, err)
	}
	if err := keyOut.Close(); err != nil {
		log.Fatalf("error closing %s: %s", keyFile, err)
	}

	return certFile, keyFile
}

func publicKey(priv interface{}) interface{} {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &k.PublicKey
	case *ecdsa.PrivateKey:
		return &k.PublicKey
	default:
		return nil
	}
}

func pemBlockForKey(priv interface{}) *pem.Block {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}
	case *ecdsa.PrivateKey:
		b, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Unable to marshal ECDSA private key: %v", err)
			os.Exit(2)
		}
		return &pem.Block{Type: "EC PRIVATE KEY", Bytes: b}
	default:
		return nil
	}
}

// OPT FUNCTIONS

func generateClientOpts(endpoints []*ua.EndpointDescription, certFile, keyFile, policy, mode, auth, username, password string, requestTimeout time.Duration) []opcua.Option {
	opts := []opcua.Option{}
	appuri := "urn:circonus:gopcua:client"
	appname := "Circonus"

	// ApplicationURI is automatically read from the cert so is not required if a cert if provided
	opts = append(opts, opcua.ApplicationURI(appuri))
	opts = append(opts, opcua.ApplicationName(appname))

	opts = append(opts, opcua.RequestTimeout(requestTimeout))

	if certFile == "" && keyFile == "" {
		if policy != none || mode != none {
			certFile, keyFile = generateCert(appuri, 2048, certFile, keyFile, (365 * 24 * time.Hour))
		}
	}

	var cert []byte
	if certFile != "" && keyFile != "" {
		debug.Printf("Loading cert/key from %s/%s", certFile, keyFile)
		c, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			log.Printf("Failed to load certificate: %s", err)
		} else {
			pk, ok := c.PrivateKey.(*rsa.PrivateKey)
			if !ok {
				log.Fatalf("Invalid private key")
			}
			cert = c.Certificate[0]
			opts = append(opts, opcua.PrivateKey(pk), opcua.Certificate(cert))
		}
	}

	var secPolicy string
	switch {
	case policy == auto:
		// set it later
	case strings.HasPrefix(policy, ua.SecurityPolicyURIPrefix):
		secPolicy = policy
		policy = ""
	case policy == none || policy == "Basic128Rsa15" || policy == "Basic256" || policy == "Basic256Sha256" || policy == "Aes128_Sha256_RsaOaep" || policy == "Aes256_Sha256_RsaPss":
		secPolicy = ua.SecurityPolicyURIPrefix + policy
		policy = ""
	default:
		log.Fatalf("Invalid security policy: %s", policy)
	}

	// Select the most appropriate authentication mode from server capabilities and user input
	authMode, authOption := generateAuth(auth, cert, username, password)
	opts = append(opts, authOption)

	var secMode ua.MessageSecurityMode
	switch strings.ToLower(mode) {
	case auto:
	case strings.ToLower(none):
		secMode = ua.MessageSecurityModeNone
		mode = ""
	case "sign":
		secMode = ua.MessageSecurityModeSign
		mode = ""
	case "signandencrypt":
		secMode = ua.MessageSecurityModeSignAndEncrypt
		mode = ""
	default:
		log.Fatalf("Invalid security mode: %s", mode)
	}

	// Allow input of only one of sec-mode,sec-policy when choosing 'None'
	if secMode == ua.MessageSecurityModeNone || secPolicy == ua.SecurityPolicyURINone {
		secMode = ua.MessageSecurityModeNone
		secPolicy = ua.SecurityPolicyURINone
	}

	// Find the best endpoint based on our input and server recommendation (highest SecurityMode+SecurityLevel)
	var serverEndpoint *ua.EndpointDescription
	switch {
	case mode == auto && policy == auto: // No user selection, choose best
		for _, e := range endpoints {
			if serverEndpoint == nil || (e.SecurityMode >= serverEndpoint.SecurityMode && e.SecurityLevel >= serverEndpoint.SecurityLevel) {
				serverEndpoint = e
			}
		}

	case mode != auto && policy == auto: // User only cares about mode, select highest securitylevel with that mode
		for _, e := range endpoints {
			if e.SecurityMode == secMode && (serverEndpoint == nil || e.SecurityLevel >= serverEndpoint.SecurityLevel) {
				serverEndpoint = e
			}
		}

	case mode == auto && policy != auto: // User only cares about policy, select highest securitylevel with that policy
		for _, e := range endpoints {
			if e.SecurityPolicyURI == secPolicy && (serverEndpoint == nil || e.SecurityLevel >= serverEndpoint.SecurityLevel) {
				serverEndpoint = e
			}
		}

	default: // User cares about both
		for _, e := range endpoints {
			if e.SecurityPolicyURI == secPolicy && e.SecurityMode == secMode && (serverEndpoint == nil || e.SecurityLevel >= serverEndpoint.SecurityLevel) {
				serverEndpoint = e
			}
		}
	}

	if serverEndpoint == nil { // Didn't find an endpoint with matching policy and mode.
		log.Printf("unable to find suitable server endpoint with selected sec-policy and sec-mode")
		log.Fatalf("quitting")
	} else {
		secPolicy = serverEndpoint.SecurityPolicyURI
		secMode = serverEndpoint.SecurityMode
	}

	// Check that the selected endpoint is a valid combo
	err := validateEndpointConfig(endpoints, secPolicy, secMode, authMode)
	if err != nil {
		log.Fatalf("error validating input: %s", err)
	}

	opts = append(opts, opcua.SecurityFromEndpoint(serverEndpoint, authMode))
	return opts
}

func generateAuth(a string, cert []byte, un, pw string) (ua.UserTokenType, opcua.Option) {
	var err error

	var authMode ua.UserTokenType
	var authOption opcua.Option
	switch strings.ToLower(a) {
	case "anonymous":
		authMode = ua.UserTokenTypeAnonymous
		authOption = opcua.AuthAnonymous()

	case "username":
		authMode = ua.UserTokenTypeUserName

		if un == "" {
			if err != nil {
				log.Fatalf("error reading username input: %s", err)
			}
		}

		if pw == "" {
			if err != nil {
				log.Fatalf("error reading username input: %s", err)
			}
		}

		authOption = opcua.AuthUsername(un, pw)

	case "certificate":
		authMode = ua.UserTokenTypeCertificate
		authOption = opcua.AuthCertificate(cert)

	case "issuedtoken":
		// todo: this is unsupported, fail here or fail in the opcua package?
		authMode = ua.UserTokenTypeIssuedToken
		authOption = opcua.AuthIssuedToken([]byte(nil))

	default:
		log.Printf("unknown auth-mode, defaulting to Anonymous")
		authMode = ua.UserTokenTypeAnonymous
		authOption = opcua.AuthAnonymous()

	}

	return authMode, authOption
}

func validateEndpointConfig(endpoints []*ua.EndpointDescription, secPolicy string, secMode ua.MessageSecurityMode, authMode ua.UserTokenType) error {
	for _, e := range endpoints {
		if e.SecurityMode == secMode && e.SecurityPolicyURI == secPolicy {
			for _, t := range e.UserIdentityTokens {
				if t.TokenType == authMode {
					return nil
				}
			}
		}
	}

	return errors.Errorf("server does not support an endpoint with security : %s , %s", secPolicy, secMode)
}
