# aiHelpDesk: Sample log of deploying aiHelpDesk in non-K8s env from source (by cloning the repo)

See [VM-based Deployment README](README.md) for background.

  ### 2.4 SRE bot demo: Deployment from source (by cloning the repo)

Here's an example of an SRE bot detecting that the `db.example.com` going offline, which results in a failure to establish a connection. As a result, aiHelpDesk automatically records an incident and creates a troubelshooting bundle to investigate further either interally or by sending to a vendor:

```
  docker run --rm --network docker-compose_default helpdesk:latest /usr/local/bin/srebot -gateway http://gateway:8080 -conn 'host=db.example.com port=5432 dbname=myapp user=admin'
```

Sample log of running the above commands:

```
[boris@ ~/helpdesk]$ docker run --rm --network docker-compose_default helpdesk:latest /usr/local/bin/srebot -gateway http://gateway:8080 -conn 'host=db.example.com port=5432 dbname=myapp user=admin'
[02:51:08] ── Phase 1: Agent Discovery ──────────────────────────
[02:51:08] GET /api/v1/agents
[02:51:08] Found 3 agents: k8s_agent, incident_agent, postgres_database_agent

[02:51:08] ── Phase 2: Health Check ─────────────────────────────
[02:51:08] POST /api/v1/db/check_connection
[02:51:11] Anomaly detected: "...s you provided.
---
ERROR — check_connection failed for host=db..."

[02:51:11] ── Phase 3: AI Diagnosis ─────────────────────────────
[02:51:11] POST /api/v1/query  agent=database
[02:51:11] Prompt: "Users are reporting database connectivity issues. The connection_string is `host=db.example.com port=5432 dbname=myapp u..."
[02:51:16] Agent response (1838 chars):
           ┌──────────────────────────────────────────────────────────────────────┐
           │ I'll investigate the connectivity issue. Let me start by testing     │
           │ the connection to your database.                                     │
           │ ---                                                                  │
           │ ERROR — check_connection failed for `host=db.example.com           │
           │ port=5432 dbname=myapp user=admin`                                   │
           │                                                                      │
           │ ```                                                                  │
           │ connection failed: The hostname in the connection string could not   │
           │ be resolved. Check for typos in the 'host' parameter and ensure DNS  │
           │ is working.                                                          │
           │                                                                      │
           │ Raw error: psql: error: could not translate host name                │
           │ "db.example.com" to address: Name or service not known               │
           │ ```                                                                  │
           │                                                                      │
           │ This means: The hostname `db.example.com` cannot be resolved by DNS. │
           │                                                                      │
           │ ---                                                                  │
           │                                                                      │
           │ ## Findings                                                          │
           │                                                                      │
           │ The database connectivity issue is due to a **DNS resolution         │
           │ failure**. The system cannot translate the hostname                  │
           │ `db.example.com` to an IP address.                                   │
           │                                                                      │
           │ ### Likely causes:                                                   │
           │ 1. **DNS misconfiguration** — The DNS server cannot resolve the    │
           │ hostname                                                             │
           │ 2. **Typo in hostname** — "db.example.com" may not be the correct  │
           │ hostname                                                             │
           │ 3. **Network/DNS outage** — The DNS service is temporarily         │
           │ unavailable                                                          │
           │ 4. **Network connectivity issue** — The client cannot reach the    │
           │ DNS server                                                           │
           │                                                                      │
           │ ### Recommended next steps:                                          │
           │ 1. **Verify the hostname** — Confirm with your database            │
           │ administrator that `db.example.com` is the correct hostname          │
           │ 2. **Test DNS resolution** — From the client machine, run:         │
           │    ```bash                                                           │
           │    nslookup db.example.com                                           │
           │    # or                                                              │
           │    dig db.example.com                                                │
           │    # or                                                              │
           │    ping db.example.com                                               │
           │    ```                                                               │
           │ 3. **Check DNS configuration** — Ensure your system's DNS servers  │
           │ are properly configured                                              │
           │ 4. **Check network connectivity** — Verify you can reach the       │
           │ network where the database resides                                   │
           │ 5. **Test with an IP address** — If you know the database IP, try  │
           │ connecting directly using the IP instead of the hostname to isolate  │
           │ the DNS issue                                                        │
           │                                                                      │
           │ Once DNS resolution is working, please provide me with the           │
           │ corrected connection string or let me know when the issue is         │
           │ resolved, and I can run a full diagnostic.                           │
           └──────────────────────────────────────────────────────────────────────┘

[02:51:16] ── Phase 4: Create Incident Bundle ───────────────────
[02:51:16] POST /api/v1/incidents
[02:51:16]   infra_key:    srebot-demo
[02:51:16]   callback_url: http://172.19.0.6:9090/callback
[02:51:21] Incident agent responded (825 chars)

[02:51:21] ── Phase 5: Awaiting Callback ────────────────────────
[02:51:21] Listening on :9090 for POST /callback ...
[02:51:21] Callback received!
[02:51:21]   incident_id: 0af54dc1
[02:51:21]   bundle_path: /data/incidents/incident-0af54dc1-20260201-025117.tar.gz
[02:51:21]   layers:      [database, os, storage]
[02:51:21]   errors:      17
[02:51:21]     - database/version.txt: psql failed: exit status 2
Output: psql: error: could not translate host name "db.example.com" to address: Name or service not known
...
[02:51:21] Done.
```
