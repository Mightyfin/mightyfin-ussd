USSD Cloudflare edge and proposed callback pilot

Cloudflare published application:
  Hostname: ussd.mightyfinance.co.zm
  Path: empty
  Type: HTTP
  Service URL: http://127.0.0.1:28092
  Additional settings: defaults

Topology:
  Cloudflare HTTPS -> host cloudflared -> Caddy 127.0.0.1:28092
  -> gateway user service 127.0.0.1:28093 -> private LMS adapter 127.0.0.1:28091
Only /ussd and /healthz are proxied. The demo simulator is never exposed.
Caddy uses host networking to reach the user service, but listens ONLY on loopback.
Do not point the tunnel at ports 28090, 28091 or 28093 directly.

Gateway binary: /home/mightyfin/.local/lib/mightyfin-ussd/ussdgateway
Unit: deploy/mightyfin-ussd-gateway.service (user systemd; enabled at boot)
Private config: /home/mightyfin/.config/mightyfin/ussd-gateway/runtime.env
Private operator credential: same directory, operator.key (0600)
The key is separate from the LMS service credential and customer tokens.
To rotate it: replace operator.key with a random 32+ character secret, restart
mightyfin-ussd-gateway.service, then securely distribute the replacement. Existing
sessions end on restart. Never commit credentials or log callback headers/bodies.

Build:
  go build -o /home/mightyfin/.local/lib/mightyfin-ussd/ussdgateway.next ./cmd/ussdgateway
  mv /home/mightyfin/.local/lib/mightyfin-ussd/ussdgateway.next /home/mightyfin/.local/lib/mightyfin-ussd/ussdgateway
  systemctl --user restart mightyfin-ussd-gateway.service
Deploy edge:
  docker compose -f deploy/gateway/compose.yaml up -d
Caddy admin API is off; recreate/restart its container after changing Caddyfile.
The image needs NET_BIND_SERVICE for the executable's file capability, even when
using a high port. Other capabilities are dropped. Filesystem is read-only.

See CONTRACT.txt for exact callback format, pilot limits and operator test steps.
callback_ready=true means the proposed callback is implemented, NOT that Hobbiton
has accepted it, assigned a live code, or that financial/customer onboarding flows
are enabled. Health is liveness; the operator must perform end-to-end acceptance.

Rollback/disable public pilot:
  docker compose -f deploy/gateway/compose.yaml down
  systemctl --user disable --now mightyfin-ussd-gateway.service
This leaves the LMS adapter, Admin LMS and demo simulator unchanged.

Payment extension: options 3/4 now use the separate private LMS payment module on
localhost:28094. See ../payments/README.txt for identity trust, tests, accounting
activation blocker and rollback. The earlier read-only description applies only
to the original catalogue/application-status routes.
