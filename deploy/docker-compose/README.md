# aiHelpDesk: Deployment for VM-based (non-K8s) databases

## 1. Deployment from binary

There are two ways to deploy and run aiHelpDesk on non-K8s environments, i.e. VMs or bare metal. In both cases the first step is downloading the right tarball for your platform from [here](https://github.com/borisdali/helpdesk/releases/). Then...

  ### 1.1 The Docker route (relies on Docker Compose with the pull of the pre-built image from GHCR)

```
  tar xzf helpdesk-v1.0.0-deploy.tar.gz
  cd helpdesk-v1.0.0-deploy/docker-compose
  cp .env.example .env
  cp infrastructure.json.example infrastructure.json
  # edit both files
  docker compose up -d
```

  ### 1.2 The non-Docker route to run the pre-built binaries on a host

```
  tar xzf helpdesk-v0.1.0-darwin-arm64.tar.gz   # pick your platform
  cd helpdesk-v0.1.0-darwin-arm64

  # Option A: set env vars directly
  export HELPDESK_MODEL_VENDOR=anthropic
  export HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001
  export HELPDESK_API_KEY=<your-API-key>
  ./startall.sh

  # Option B: use a .env file
  cat > .env <<EOF
  HELPDESK_MODEL_VENDOR=anthropic
  HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001
  HELPDESK_API_KEY=your-key
  HELPDESK_INFRA_CONFIG=./infrastructure.json
  EOF
  ./startall.sh
```

  The script:
  - Sources .env if present
  - Validates required env vars
  - Starts all 3 agents + gateway in the background (logs to /tmp/helpdesk-*.log)
  - Launches the interactive orchestrator in the foreground
  - Cleans up all background processes on exit/Ctrl-C
  - --no-repl runs headless (gateway only, no orchestrator)
  - --stop kills any running helpdesk processes

N.B: Please note that the binary tarballs expect `psql` and `kubectl` already installed on the host — those are baked into the Docker image (see option 1.1), but not into the Go binaries.


## 2. Deployment from source (by cloning the repo):

 ### 2.1 Clone the aiHelpDesk repo :-)

 ### 2.2 Run Docker Compose to start all aiHelpDesk agents

```
  docker compose -f deploy/docker-compose/docker-compose.yaml up -d
```

See the [sample log](INSTALL_from_source_sample_log.md)  of running the above commands.


  ## 2.3 Interactive/Human session: Deployment from source (by cloning the repo)

Once all the aiHelpDesk agents are up, copy and adjust the `.env` file and the `infrastructure.json` file.
The former contains the LLM info (e.g. Anthropic, Gemini, etc.), while the latter file contains the databases that aiHelpDesk needs to be are of.

```
  cp deploy/docker-compose/.env.example deploy/docker-compose/.env
  cp deploy/docker-compose/infrastructure.json.example deploy/docker-compose/infrastructure.json
```

If you prefer the list of databases (that go into `infrastructure.json` file) to reside elsewhere, set the `HELPDESK_INFRA_CONFIG` env variable as explained in `.env` file (ignore the Kube stuff, which isn't relevant for the VM deployment):

```
[boris@ ~/helpdesk/deploy/docker-compose]$ cat .env.example
# Model configuration (required)
HELPDESK_MODEL_VENDOR=anthropic
HELPDESK_MODEL_NAME=claude-haiku-4-5-20251001
HELPDESK_API_KEY=<your-api-key-here>

# Kubeconfig path for K8s and incident agents (optional)
KUBECONFIG=~/.kube/config

# Infrastructure inventory for the orchestrator (optional).
# Path to a JSON file describing your database servers, K8s clusters, and VMs.
# Copy the example and edit it with your real servers:
#   cp infrastructure.json.example infrastructure.json
HELPDESK_INFRA_CONFIG=./infrastructure.json
```

Next, as a human operator, run the interactive session by invoking the Orchestator:

```
  docker compose -f deploy/docker-compose/docker-compose.yaml --profile interactive run orchestrator
```

See the [sample log](INSTALL_from_source_sample_interactive_log.md)  of running the above commands.


  ### 2.4 SRE bot demo: Deployment from source (by cloning the repo)

Here's an example of an SRE bot detecting that the `db.example.com` is going offline, which results in a failure to establish a connection. As a result, aiHelpDesk automatically records an incident and creates a troubelshooting bundle to investigate further either interally or by sending to a vendor:

```
  docker run --rm --network docker-compose_default helpdesk:latest /usr/local/bin/srebot -gateway http://gateway:8080 -conn 'host=db.example.com port=5432 dbname=myapp user=admin'
```

See the [sample log](INSTALL_from_source_sample_SRE_bot_log.md)  of running the above commands.

