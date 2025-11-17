# ses-smtpd-relay

An SMTP daemon that bridges legacy mail systems with Amazon SES by converting SMTP protocol messages into SES API calls.

## Quick Start

```bash
# Run with defaults (port 2500)
./ses-smtpd-relay

# Custom port
./ses-smtpd-relay :3025
```

## Configuration

### AWS Setup
Ensure AWS credentials are configured via:
- EC2/ECS/EKS instance roles
- `~/.aws/credentials` file  
- Environment variables: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`
- Cross-account role assumption: `AWS_ROLE_ARN`, `AWS_ROLE_SESSION_NAME`

Required IAM permission: `ses:SendRawEmail`

### Command Options
```
--configuration-set-name    SES configuration set for tracking
--enable-prometheus         Start metrics server
--prometheus-bind          Metrics server address (:2501)
--enable-health-check      Start health endpoint  
--health-check-bind        Health server address (:3000)
--version                  Show version info
```

## Endpoints

**Health Check** (when enabled):
```
GET /health
→ {"name": "ses-smtpd-relay", "status": "ok", "version": "..."}
```

**Metrics** (when enabled):
```
GET /metrics
→ Prometheus format metrics
```

## Metrics

- `smtpd_email_send_success_total` - Successful deliveries
- `smtpd_email_send_fail_total` - Failed attempts (labeled by error type)
- `smtpd_ses_error_total` - SES API errors

## Limitations

- No authentication required (design choice for internal networks)
- 40MB message size limit (SES v2 API constraint)
- No TLS/SSL support

## Build

```bash
make ses-smtpd-relay
```

## Docker

```bash
make docker
docker run -p 2500:2500 ghcr.io/ORG/ses-smtpd-relay:latest
```

## License

MIT - see LICENSE.txt
