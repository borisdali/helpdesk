# aiHelpDesk sample log of JWT authn on K8s

Please see [here](IDENTITY.md) for the full documentation reference
on aiHelpDesk Identity & Access system, a sub-module of
[AI Governance](AIGOVERNANCE.md).

The sample log presented here is how JWT authn can be tested on K8s with
no extenal dependencies (tested on Mac Apple Silicon arm64 VM)
. In reality you'd want to have a proper IdP
setup (e.g. Okta, Auth0, Keycloak, etc), but for testing the built-in
`jwttest` mock is sufficient:


```
[boris@ ~/helpdesk]$ mkdir /tmp/jwttest-ctx
[boris@ ~/helpdesk]$ cat > /tmp/jwttest-ctx/Dockerfile <<'EOF'
> FROM alpine:3.19
> COPY jwttest /usr/local/bin/jwttest
> EXPOSE 9999
> ENTRYPOINT ["/usr/local/bin/jwttest"]
> EOF

[boris@ ~/helpdesk]$ GOOS=linux GOARCH=arm64 go build -o /tmp/jwttest-ctx/jwttest-linux ./cmd/jwttest/
[boris@ ~/helpdesk]$ docker build -t jwttest:local /tmp/jwttest-ctx/
[+] Building 1.0s (7/7) FINISHED                                                                                                                                                                                             docker:desktop-linux
 => [internal] load build definition from Dockerfile                                                                                                                                                                                         0.0s
 => => transferring dockerfile: 177B                                                                                                                                                                                                         0.0s
 => [internal] load metadata for docker.io/library/alpine:3.19                                                                                                                                                                               0.5s
 => [internal] load .dockerignore                                                                                                                                                                                                            0.0s
 => => transferring context: 2B                                                                                                                                                                                                              0.0s
 => [internal] load build context                                                                                                                                                                                                            0.1s
 => => transferring context: 7.95MB                                                                                                                                                                                                          0.1s
 => CACHED [1/2] FROM docker.io/library/alpine:3.19@sha256:6baf43584bcb78f2e5847d1de515f23499913ac9f12bdf834811a3145eb11ca1                                                                                                                  0.0s  => => resolve docker.io/library/alpine:3.19@sha256:6baf43584bcb78f2e5847d1de515f23499913ac9f12bdf834811a3145eb11ca1                                                                                                                         0.0s  => [2/2] COPY jwttest /usr/local/bin/jwttest                                                                                                                                                                                                0.1s  => exporting to image                                                                                                                                                                                                                       0.2s
 => => exporting layers                                                                                                                                                                                                                      0.2s
 => => exporting manifest sha256:5512a1b10f62377f6ee84437f6e6d45d25c1124e91652f1a336cb82410e23762                                                                                                                                            0.0s
 => => exporting config sha256:0904550aa22b683f2be8db703346ca4563d0875af916e0a989040ee24524e1f6                                                                                                                                              0.0s
 => => exporting attestation manifest sha256:a71f364b073156eb231de04a94f8031c61bd3b4f4b5a6dbba8b2dfb2ed8f28a9                                                                                                                                0.0s
 => => exporting manifest list sha256:75cc5288270ae156d0bc1afe05d6e153606e394953d2975294371efa6199d2b8                                                                                                                                       0.0s
 => => naming to docker.io/library/jwttest:local                                                                                                                                                                                             0.0s
 => => unpacking to docker.io/library/jwttest:local                                                                                                                                                                                          0.0s

[boris@ ~/helpdesk]$ docker images|head -2
REPOSITORY                      TAG                        IMAGE ID       CREATED          SIZE
jwttest                         local                      75cc5288270a   11 seconds ago   24.1MB

[boris@ ~/helpdesk]$ kubectl -n helpdesk-system run jwttest --image=jwttest:local --image-pull-policy=Never --port=9999 -- -addr 0.0.0.0 -iss https://idp.example.com -aud helpdesk -groups dba,sre
pod/jwttest created

[boris@ ~/helpdesk]$ k -nhelpdesk-system get pod/jwttest -owide
NAME      READY   STATUS    RESTARTS   AGE   IP             NODE       NOMINATED NODE   READINESS GATES
jwttest   1/1     Running   0          8s    10.244.1.252   minikube   <none>           <none>

[boris@ /tmp/helpdesk/helpdesk-v0.7.0-deploy/helm/helpdesk]$ kubectl -n helpdesk-system logs jwttest
JWKS server:  http://localhost:9999/jwks.json
Subject:      alice@example.com
Groups:       dba,sre
Expires:      2026-03-20T16:25:06Z

Start gateway with:
  HELPDESK_IDENTITY_PROVIDER=jwt \
  HELPDESK_JWT_JWKS_URL=http://localhost:9999/jwks.json \
  HELPDESK_JWT_ISSUER=https://idp.example.com \
  HELPDESK_JWT_AUDIENCE=helpdesk \
  HELPDESK_JWT_ROLES_CLAIM=groups \
  go run ./cmd/gateway

eyJhbGciOiJSUzI1NiIsImtpZCI6ImRldi1rZXktMSIsInR5cCI6IkpXVCJ9.eyJhdWQiOiJoZWxwZGVzayIsImV4cCI6MTc3NDAyMzkwNiwiZ3JvdXBzIjpbImRiYSIsInNyZSJdLCJpYXQiOjE3NzQwMjAzMDYsImlzcyI6Imh0dHBzOi8vaWRwLmV4YW1wbGUuY29tIiwic3ViIjoiYWxpY2VAZXhhbXBsZS5jb20ifQ.  Xi3DgN2-FBvdsuZu7nU6-hC6JXdeXH3euh8fSjevzjaql0B7a9_IQBYjhUuhs7u-62PO5BxBc3d7dI99iQpmvgcEU4XaXRUgS6NKgwnnvoJtaTc6VfN5y4W4U7VL8RoxAfCGXnGjSHCDhBxhuLJ6llm78uF8WH-PEHjGX72Gp59eqNF6ic_Pto4O77agaWGBEK1vYTXNiq0S3fCdPrsGFI9EXKh-gt-                   cLRaCKH_UnLUgipmVvxfBCpifZ8KlvgKedtKh0p5TfNxwpC62fw4EdhEm36qwqIl6AUWFKtZDYsXcoq4VeFGbb86MmGdWvgzzX0wq6fquSzd1SWnUr083rA
Then send requests with:
  TOKEN="eyJhbGciOiJSUzI1NiIsImtpZCI6ImRldi1rZXktMSIsInR5cCI6IkpXVCJ9.                                                                                                                                                                            eyJhdWQiOiJoZWxwZGVzayIsImV4cCI6MTc3NDAyMzkwNiwiZ3JvdXBzIjpbImRiYSIsInNyZSJdLCJpYXQiOjE3NzQwMjAzMDYsImlzcyI6Imh0dHBzOi8vaWRwLmV4YW1wbGUuY29tIiwic3ViIjoiYWxpY2VAZXhhbXBsZS5jb20ifQ.Xi3DgN2-FBvdsuZu7nU6-                                          hC6JXdeXH3euh8fSjevzjaql0B7a9_IQBYjhUuhs7u-62PO5BxBc3d7dI99iQpmvgcEU4XaXRUgS6NKgwnnvoJtaTc6VfN5y4W4U7VL8RoxAfCGXnGjSHCDhBxhuLJ6llm78uF8WH-PEHjGX72Gp59eqNF6ic_Pto4O77agaWGBEK1vYTXNiq0S3fCdPrsGFI9EXKh-gt-                                        cLRaCKH_UnLUgipmVvxfBCpifZ8KlvgKedtKh0p5TfNxwpC62fw4EdhEm36qwqIl6AUWFKtZDYsXcoq4VeFGbb86MmGdWvgzzX0wq6fquSzd1SWnUr083rA"
  curl -H "Authorization: Bearer $TOKEN" ...

Serving JWKS (Ctrl-C to stop)...
```

Configure aiHelpDesk to use JWT as the authn provider:

```
[boris@ /tmp/helpdesk/helpdesk-v0.7.0-deploy/helm/helpdesk]$ grep -A27 identity values.yaml
  identity:
    # Identity provider: none (default), static, or jwt.
    # "none"   = accept X-User / X-Roles headers without verification (dev only).
    # "static" = verify users against usersConfig or usersSecret.
    # "jwt"    = verify JWT Bearer tokens.
    provider: jwt

    # Inline users.yaml content — rendered into a chart-managed Secret.
    # Copy from users.example.yaml and edit. Ignored when provider != static.
    # usersConfig: |
    #   users:
    #     - id: alice@example.com
    #       roles: [dba, sre]
    usersConfig: ""

    # Name of a pre-existing Secret with a "users.yaml" key.
    # Use instead of usersConfig when managing secrets out-of-band.
    # Ignored when usersConfig is set.
    usersSecret: "helpdesk-users"	--> this was the artifact of testing the static authn, can be ignored here

    jwt:
      # Required when provider=jwt.
      jwksURL: "http://jwttest:9999/jwks.json"
      issuer: "https://idp.example.com"
      audience: "helpdesk"
      rolesClaim: "groups"
      cacheTTL: ""
```

Run the tests:

```
[boris@ /tmp/helpdesk/helpdesk-v0.7.0-deploy/helm/helpdesk]$ TOKEN=$(kubectl -n helpdesk-system logs jwttest | grep '^eyJ')
[boris@ /tmp/helpdesk/helpdesk-v0.7.0-deploy/helm/helpdesk]$ /tmp/helpdesk-client --api-key "$TOKEN" --message "is pg-cluster-minkube up?" --purpose diagnostic
⠇  Thinking…I'll check if pg-cluster-minkube is up by testing the connection.
Yes, **pg-cluster-minkube is up and running**. Here are the details:

- **Status**: ✅ Connection successful
- **PostgreSQL Version**: 16.2 (Debian) on aarch64 (ARM)
- **Current Database**: app
- **Current User**: app
- **Server Address**: 10.244...:5432

The database is healthy and accepting connections.
[trace: tr_e0c61426-ab6  2026-03-20 11:57:35]
```

Basic aiHelpDesk Journey and the audit trail summary:

```
[boris@ /tmp/helpdesk/helpdesk-v0.7.0-deploy/helm/helpdesk]$ curl -s "http://localhost:1199/v1/journeys?trace_id=tr_e0c61426-ab6"|jq
Handling connection for 1199
[
  {
    "trace_id": "tr_e0c61426-ab6",
    "started_at": "2026-03-20T15:57:32.574971379Z",
    "ended_at": "2026-03-20T15:57:33.544081963Z",
    "duration_ms": 969,
    "user_id": "alice@example.com",
    "user_query": "is pg-cluster-minkube up?",
    "purpose": "diagnostic",
    "agent": "postgres_database_agent",
    "tools_used": [
      "check_connection"
    ],
    "outcome": "success",
    "event_count": 6
  }
]

root@helpdesk-auditd-766bcc4c8c-69kht:/# sqlite3 --header --column /data/audit/audit.db "SELECT id, timestamp, event_id, event_type, trace_id, session_agent sagent, session_id, action_class, tool_name, user_id,          decision_agent,        decision_confidence confid, outcome_status outcome, purpose    pur, purpose_note purnote FROM audit_events WHERE trace_id='tr_5107d5fe-030' ORDER BY timestamp DESC"
id  timestamp                       event_id       event_type       trace_id         sagent  session_id        action_class  tool_name         user_id            decision_agent           confid  outcome  pur         purnote
--  ------------------------------  -------------  ---------------  ---------------  ------  ----------------  ------------  ----------------  -----------------  -----------------------  ------  -------  ----------  -------
68  2026-03-20T15:42:33.934155171Z  tool_de959794  tool_execution   tr_5107d5fe-030          dbagent_3c4d4dbd  read          check_connection                     postgres_database_agent          success
67  2026-03-20T15:42:33.827272421Z  pol_66ca30e4   policy_decision  tr_5107d5fe-030          tr_5107d5fe-030   read                                                                                allow    diagnostic
66  2026-03-20T15:42:33.822876963Z  inv_1209013d   tool_invoked     tr_5107d5fe-030          dbagent_3c4d4dbd  read
65  2026-03-20T15:42:33.80323413Z   rsn_3bb831f8   agent_reasoning  tr_5107d5fe-030          dbagent_3c4d4dbd
64  2026-03-20T15:42:31.86574567Z   req_bb90d940   gateway_request  tr_5107d5fe-030          asess_4020291d                                    alice@example.com  postgres_database_agent                   diagnostic
69  2026-03-20T15:42:31.862798837Z  gw_fa10d8bb    gateway_request  tr_5107d5fe-030          600e292d          unknown                         alice@example.com  postgres_database_agent  1.0     success  diagnostic
```
