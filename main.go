package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/emersion/go-smtp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var version string

const (
	SesSizeLimit = 40000000 // 40MB limit for SES v2 API
	DefaultAddr  = ":2500"
)

var (
	emailSent = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "smtpd",
		Name:      "email_send_success_total",
		Help:      "Total number of successfuly sent emails",
	})
	emailError = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "smtpd",
		Name:      "email_send_fail_total",
		Help:      "Total number emails that failed to send",
	}, []string{"type"})
	sesError = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "smtpd",
		Name:      "ses_error_total",
		Help:      "Total number errors with SES",
	})
)

// Backend implements smtp.Backend
type Backend struct {
	sesClient         *ses.Client
	configSetName     *string
}

// NewSession implements smtp.Backend
func (b *Backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &Session{
		backend: b,
		conn:    c,
	}, nil
}

// Session implements smtp.Session
type Session struct {
	backend       *Backend
	conn          *smtp.Conn
	from          string
	recipients    []string
	data          []byte
}

// AuthPlain implements smtp.Session (no-op for unauthenticated server)
func (s *Session) AuthPlain(username, password string) error {
	return nil
}

// Mail implements smtp.Session
func (s *Session) Mail(from string, opts *smtp.MailOptions) error {
	s.from = from
	return nil
}

// Rcpt implements smtp.Session
func (s *Session) Rcpt(to string, opts *smtp.RcptOptions) error {
	s.recipients = append(s.recipients, to)
	return nil
}

// Data implements smtp.Session
func (s *Session) Data(r io.Reader) error {
	if len(s.recipients) == 0 {
		emailError.With(prometheus.Labels{"type": "no valid recipients"}).Inc()
		return &smtp.SMTPError{
			Code:         554,
			EnhancedCode: smtp.EnhancedCode{5, 5, 1},
			Message:      "Error: no valid recipients",
		}
	}

	// Read message data with size limit
	data, err := io.ReadAll(io.LimitReader(r, SesSizeLimit+1))
	if err != nil {
		emailError.With(prometheus.Labels{"type": "read error"}).Inc()
		return &smtp.SMTPError{
			Code:         451,
			EnhancedCode: smtp.EnhancedCode{4, 5, 1},
			Message:      "Temporary server error reading message",
		}
	}

	if len(data) > SesSizeLimit {
		emailError.With(prometheus.Labels{"type": "minimum message size exceed"}).Inc()
		log.Printf("message size %d exceeds SES limit of %d", len(data), SesSizeLimit)
		return &smtp.SMTPError{
			Code:         554,
			EnhancedCode: smtp.EnhancedCode{5, 5, 1},
			Message:      "Error: maximum message size exceeded",
		}
	}

	s.data = data

	// Send via SES
	input := &ses.SendRawEmailInput{
		ConfigurationSetName: s.backend.configSetName,
		Source:               &s.from,
		Destinations:         s.recipients,
		RawMessage:           &types.RawMessage{Data: s.data},
	}

	_, err = s.backend.sesClient.SendRawEmail(context.TODO(), input)
	if err != nil {
		log.Printf("ERROR: ses: %v", err)
		emailError.With(prometheus.Labels{"type": "ses error"}).Inc()
		sesError.Inc()
		return &smtp.SMTPError{
			Code:         451,
			EnhancedCode: smtp.EnhancedCode{4, 5, 1},
			Message:      "Temporary server error. Please try again later",
		}
	}

	// Log successful send
	configSetInfo := "no config set"
	if s.backend.configSetName != nil {
		configSetInfo = fmt.Sprintf("config set: %s", *s.backend.configSetName)
	}
	log.Printf("sending message from %s to %v (%s)", s.from, s.recipients, configSetInfo)
	emailSent.Inc()

	return nil
}

// Reset implements smtp.Session
func (s *Session) Reset() {
	s.from = ""
	s.recipients = nil
	s.data = nil
}

// Logout implements smtp.Session
func (s *Session) Logout() error {
	return nil
}

func validateConfigurationSet(ctx context.Context, sesClient *ses.Client, configSetName string) error {
	_, err := sesClient.DescribeConfigurationSet(ctx, &ses.DescribeConfigurationSetInput{
		ConfigurationSetName: &configSetName,
	})
	return err
}

func makeSesClient(ctx context.Context) (*ses.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}

	// Check for role assumption from environment variables
	if roleArn := os.Getenv("AWS_ROLE_ARN"); roleArn != "" {
		sessionName := os.Getenv("AWS_ROLE_SESSION_NAME")
		if sessionName == "" {
			sessionName = "ses-smtpd-relay-session"
		}
		
		stsClient := sts.NewFromConfig(cfg)
		provider := stscreds.NewAssumeRoleProvider(stsClient, roleArn, func(o *stscreds.AssumeRoleOptions) {
			o.RoleSessionName = sessionName
		})
		
		cfg.Credentials = aws.NewCredentialsCache(provider)
	}

	// Log current AWS identity
	stsClient := sts.NewFromConfig(cfg)
	identity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		log.Printf("Warning: Could not verify AWS identity: %v", err)
	} else {
		log.Printf("AWS Identity - Account: %s, ARN: %s", *identity.Account, *identity.Arn)
	}

	return ses.NewFromConfig(cfg), nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	enablePrometheus := flag.Bool("enable-prometheus", false, "Enable prometheus metrics server")
	prometheusBind := flag.String("prometheus-bind", ":2501", "Address/port on which to bind Prometheus server")
	showVersion := flag.Bool("version", false, "Show program version")
	configurationSetName := flag.String("configuration-set-name", "", "Configuration set name with which SendRawEmail will be invoked")
	enableHealthCheck := flag.Bool("enable-health-check", false, "Enable health check server")
	healthCheckBind := flag.String("health-check-bind", ":3000", "Address/port on which to bind health check server")

	flag.Parse()

	if *showVersion {
		fmt.Printf("ses-smtpd-relay version %s\n", version)
		return
	}

	if *enableHealthCheck {
		sm := http.NewServeMux()
		ps := &http.Server{Addr: *healthCheckBind, Handler: sm}
		sm.Handle("/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Content-Type", "application/json")
			w.Write([]byte("{\"name\": \"ses-smtpd-relay\", \"status\": \"ok\", \"version\": \"" + version + "\"}"))
		}))
		go ps.ListenAndServe()
		log.Printf("Health check server listening on %s", *healthCheckBind)
	}

	sesClient, err := makeSesClient(ctx)
	if err != nil {
		log.Fatalf("Error creating AWS session: %s", err)
	}

	// Validate configuration set if provided
	if *configurationSetName != "" {
		if err := validateConfigurationSet(ctx, sesClient, *configurationSetName); err != nil {
			log.Fatalf("Configuration set '%s' not found or inaccessible: %s", *configurationSetName, err)
		}
		log.Printf("Configuration set '%s' validated successfully", *configurationSetName)
	}

	addr := DefaultAddr
	if flag.Arg(0) != "" {
		addr = flag.Arg(0)
	} else if flag.NArg() > 1 {
		log.Fatalf("usage: %s [listen_host:port]", os.Args[0])
	}

	if *enablePrometheus {
		sm := http.NewServeMux()
		ps := &http.Server{Addr: *prometheusBind, Handler: sm}
		sm.Handle("/metrics", promhttp.Handler())
		go ps.ListenAndServe()
	}

	var configSetPtr *string
	if *configurationSetName != "" {
		configSetPtr = configurationSetName
	}

	backend := &Backend{
		sesClient:     sesClient,
		configSetName: configSetPtr,
	}

	s := smtp.NewServer(backend)
	s.Addr = addr
	s.Domain = "localhost"
	s.AllowInsecureAuth = true // Allow plain auth over non-TLS (as per original design)

	go func() {
		log.Printf("ListenAndServe on %s", addr)
		if err := s.ListenAndServe(); err != nil {
			log.Printf("Error in ListenAndServe: %v", err)
		}
	}()

	select {
	case <-ctx.Done():
		log.Printf("SIGTERM/SIGINT received, shutting down")
		s.Close()
		os.Exit(0)
	}
}
