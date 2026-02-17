
<p align="center">
  <img alt="aiHelpDesk_logo" src="https://github.com/user-attachments/assets/9687ccd4-a2ad-4c85-9466-bcb6c006e8ac" width="40%"/>
</p>


# aiHelpDesk: AI DB SRE in a box

A Go-based AI multi-agent intelligent self-service help and support system for troubleshooting PostgreSQL and its derivative databases (like AlloyDB Omni) hosted on Kubernetes or VM infrastructure. The key features are:

* aiHelpDesk is an implementation of the shift-left support paradigm in AI-Assisted Database Management products (see next section).
* aiHelpDesk is designed for human operators as well as for the upstream agents.
* aiHelpDesk aims to prevent incidents based on active reasoning, not just help troubleshoot them after they occur.
* aiHelpDesk is equipped with the comprehensive eight-module AI Governance system.
* aiHelpDesk features a built-in incident diagnostic bundle management for vendor support.
* aiHelpDesk features a built-in fault injection framework. 
* aiHelpDesk is implemented using Google ADK (Agent Development Kit) for Go and the A2A (Agent-to-Agent) protocol for modularity and extensibility where self-contained expert agents can be added or swapped from a Marketplace in favor to those shipped with aiHelpDesk out of the box.

aiHelpDesk is designed to help customers and agents with the AI-assisted triage, root cause analysis and remediation of database related problems on K8s and VMs.

## AI-Assisted Database Management
While SaaS applications clearly have their market and cloud vendor DBaaS systems in particular are among the fastest growing and most profitable sectors on GCP, AWS and Azure, there are legitimate reasons for many customers to stay away from the black-box, vendor lock-in, cloud provider specific management systems. See [here](https://medium.com/google-cloud/databases-on-k8s-really-part-8-182259e1720f) for extensive treatment of this topic and, in particular, see the 13 specific customer expectations of the cloud provider's DBaaS and how they mostly fall short of these expectations. 

Enter the world of AI-Assisted Database Management products.

aiHelpDesk is the first product from the DDS Group that starts on the path of implementing the AI-Assisted vision of this new breed of the database management products.

## Deployment and Supported Platforms
aiHelpDesk can be deployed on K8s or VMs / bare metal. The binary packages are provided for Linux x86-64 and ARM (Graviton, Ampere), as well as for macOS (Intel and Apple Silicon).

  ### On VMs
There are two options to run aiHelpDesk on non-K8s environments, either in the Docker containers or straight on the host.

For the former, the first Docker Compose command brings up all the aiHelpDesk agents. The second command starts an interactive session of the aiHelpDesk Orchestrator to talk to a human operator:

```
  docker compose -f deploy/docker-compose/docker-compose.yaml up -d
  docker compose -f deploy/docker-compose/docker-compose.yaml --profile interactive run orchestrator
```

For the latter (i.e. for running aiHelpDesk with no Docker, straight on a host), there's a small helper `startall.sh` script that brings up all the agent Go binaries in the background including the optional Gateway, followed on with the interfactive aiHelpDesk Orchestrator session as well:

```
  ./startall.sh
```

Please be sure to set your desired LLM model (it defaults to Anthropic's Haiku), the API key and the database inventory.

See [VM-based Deployment](deploy/docker-compose/README.md) for detailed instructions on how to deploy aiHelpDesk either as binaries (simpler) or manually by cloning the repo.

  ### On K8s

```
  tar xzf helpdesk-v0.1.0-deploy.tar.gz
  kubectl create secret generic helpdesk-api-key --from-literal=api-key=<YOUR_API_KEY>
  helm install helpdesk ./helpdesk-v0.1.0-deploy/helm/helpdesk \
    --set model.vendor=anthropic \
    --set model.name=claude-haiku-4-5-20251001
```

See [K8s-based Deployment](deploy/helm/README.md) for detailed instructions on how to deploy aiHelpDesk on K8s.

## Architecture
See aiHelpDesk's [ARCHITECTURE.md](ARCHITECTURE.md) for system design, configuration, API reference, and extension guide.

## AI Governance 
aiHelpDesk is proud to feature a sophisticated AI Governanance system,
which rests on eight separate subsystems, including the full
auditing. Please see [here](AIGOVERNANCE.md) for details.

## Testing
aiHelpDesk features a comprehensive testing strategy as documented [here](testing/README.md), including a built-in fault injection testing framework, see [here](testing/FAULT_INJECTION_TESTING.md).

## Gateway REST API
In addition to the interactive orchestrator, aiHelpDesk provides a Gateway REST API for programmatic access:

```bash
# Query the system
curl -X POST http://localhost:8080/api/v1/query \
  -H "Content-Type: application/json" \
  -d '{"agent": "database", "message": "What is the server uptime?"}'

# List agents
curl http://localhost:8080/api/v1/agents

# List managed databases
curl http://localhost:8080/api/v1/databases

# Direct tool calls
curl -X POST http://localhost:8080/api/v1/db/get_server_info \
  -H "Content-Type: application/json" \
  -d '{"connection_string": "host=db.example.com port=5432 dbname=mydb user=admin"}'
```

The Gateway API is recommended for CI/CD pipelines, automation, and containerized environments. See deployment READMEs for details.

## Helper Scripts

aiHelpDesk includes helper scripts for working around the ADK REPL bug in containers:

| Script | Description |
|--------|-------------|
| `scripts/gateway-repl.sh` | Interactive REPL using the Gateway API (recommended for containers) |
| `scripts/k8s-local-repl.sh` | Run orchestrator locally with K8s agents port-forwarded |

See [scripts/README.md](scripts/README.md) for detailed usage.

## Demo app calling aiHelpDesk
aiHelpDesk can certainly be used by humans and that's what the interactive LLM-powered Orchestrator is there for. Additionally however, an upstream agent or a program can call aiHelpDesk agents directly as well.

See [a sample](cmd/srebot/README.md) of a O11y watcher program or an SRE bot that calls aiHelpDesk (via a Gateway) to understand a state of a database and ask for AI-powered diagnostic and troubleshooting.

## Sample interactive dialog with a human operator
aiHelpDesk is designed to work with humans and upstream agents alike. Here's a [sample intro dialog](INTRO_DIALOG.md) with a human operator (aka aiHelpDesk's "Hello World").

## LLM
aiHelpDesk relies on Google ADK (Agent Development Kit) for Go, which was built around Gemini models. We've extended aiHelpDesk to work with both Anthropic and Gemini models.

**Supported models:**

| Vendor | Model Name | Notes |
|--------|------------|-------|
| Anthropic | `claude-haiku-4-5-20251001` | Fast, cost-effective |
| Anthropic | `claude-sonnet-4-20250514` | Balanced performance |
| Anthropic | `claude-opus-4-5-20251101` | Most capable |
| Gemini | `gemini-2.5-flash` | Fast, recommended for most use cases |
| Gemini | `gemini-2.5-flash-lite` | Fastest, lower cost |
| Gemini | `gemini-2.5-pro` | Most capable 2.5 model |
| Gemini | `gemini-3-flash-preview` | Latest 3.0 series, fast |
| Gemini | `gemini-3-pro-preview` | Latest 3.0 series, most capable |

**Note:** Gemini 1.x and 2.0 models are retired and will return errors.

Beyond these, support for other models can be easily added (ADK's LLM interface is simple and can be implemented for other providers, just as we did for Anthropic). 

Please note that aiHelpDesk offers the flexibility for individual expert agents (e.g. a Database agent, a K8s agent, an Incident Management agent) to run with different LLMs if needed or if an agent's provider recommends or tests their agent with a particular LLM. The sample deployment scripts assumes the same LLM for all agents, but that can be easily adjusted with setting env variables before starting each agent.

Please contact aiHelpDesk if you or your customer would like to see a support for a particular LLM.

