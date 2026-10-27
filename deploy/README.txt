Private USSD deployment — 27 October 2026

Checkout: /home/mightyfin/production/mightyfin-ussd
Original source: 4d435f8bb8cddf3fc7f35d75461304653e066029
Binary: /home/mightyfin/.local/lib/mightyfin-ussd/ussd
User service: mightyfin-ussd.service
Data: /home/mightyfin/.local/share/mightyfin-ussd (0700)
Bind: http://127.0.0.1:28090
Health: /healthz returns mode=local-demo

This is a private simulator deployment, not an LMS-connected live USSD channel.
Port 8090 is already occupied by the partner API, so this deployment uses 28090.
There is no public Caddy route or Cloudflare Tunnel mapping for the simulator.

Review from your workstation using an SSH connection to this server:
  ssh -N -L 28090:127.0.0.1:28090 mightyfin@YOUR_SERVER
Then open http://127.0.0.1:28090 in your local browser.
This forwards private operator access; it is not public exposure of the demo.
Use fictional details only. The workbench includes unauthenticated demo staff controls.

Build with Go 1.26 or later:
  go test ./...
  go test -race ./...  # requires CGO and a C compiler
  go vet ./...
  go build -trimpath -o /home/mightyfin/.local/lib/mightyfin-ussd/ussd ./cmd/ussd
Install deploy/mightyfin-ussd.service into ~/.config/systemd/user/ and run:
  systemctl --user daemon-reload
  systemctl --user enable --now mightyfin-ussd.service
User lingering must be enabled for startup without login.

The wrapper holds a kernel supervisor lock before clearing any stale application
marker. Only this supervised instance may use the deployment data directory;
manual instances must use another directory or this same wrapper. SIGINT/SIGTERM
perform graceful shutdown. Restart after SIGKILL was verified with synthetic data.
Keep demo.key and state.enc together for recovery. The key beside the snapshot is
not production secret management. No production data or credentials are installed.

Verified: normal tests, race tests, go vet, health, foreign Host/Origin rejection,
missing-token rejection, normal restart and forced-crash state preservation.
Stopping or rolling back:
  systemctl --user disable --now mightyfin-ussd.service
Preserve the data directory if a review session must be recovered.
