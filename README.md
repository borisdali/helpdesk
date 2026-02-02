# aiHelpDesk: AI DB SRE in a box: a Multi-Agent Support and HelpDesk System

A Go-based AI multi-agent self-service help and support system for troubleshooting PostgreSQL and PostgreSQL based databases (like AlloyDB Omni) hosted on Kubernetes or VM infrastructure. The key features are:

* aiHelpDesk is an implementation of the shift-left support paradigm in AI-Assisted Database Management products (see next section).
* aiHelpDesk aims to prevent incidents based on active reasoning, not just help troubleshoot them after they occur.
* aiHelpDesk features a built-in incident diagnostic bundle management for vendor support.
* aiHelpDesk features a built-in failure testing injection framework. 
* aiHelpDesk is implemented using Google ADK and the A2A (Agent-to-Agent) protocol for extensibility where agents can be added or replaced in addition to those provided with aiHelpDesk out of the box.

aiHelpDesk is designed to help customers with AI-assisted triage, root cause analysis and remediation of database related problems on K8s and VMs.

## AI-Assisted Database Management
While SaaS applications clearly have their market and cloud vendor DBaaS systems in particular are among the fastest growing and most profitable sectors on GCP, AWS and Azure, there are legitimate reasons why many customers prefer to stay away from the black-box, vendor lock-in, cloud-specific management systems. See [here](https://medium.com/google-cloud/databases-on-k8s-really-part-8-182259e1720f) for extensive treatment of this topic and in particular the customer expectations of the cloud provider's DBaaS and how they often fall short of these expectations. 

Enter AI-Assisted Database Management products.

aiHelpDesk is the first product from the Dali Group that starts on the path of implementing the AI-Assisted vision of this new breed of the database management products.

## Deployment and Supported Platforms
aiHelpDesk can be deployed on K8s or VMs. The binary packages are provided for Linux x86-64 and ARM (Graviton, Ampere), as well as for macOS (Intel and Apple Silicon).

  ### On K8s

...

See [K8s-based Deployment](deploy/helm/README.md) for detailed instructions on how to deploy aiHelpDesk on K8s.

  ### On VMs
There are two options to run aiHelpDesk on non-K8s environments, either in the Docker containers or straight on the host.

For the former, this single Docker Compose command brings up all aiHelpDesk agents (except the Orchestrator, which is designed to run interactively by a human operator and hence the second command):

```
[boris@ ~/helpdesk]$ docker compose -f deploy/docker-compose/docker-compose.yaml up -d
[boris@ ~/helpdesk]$ docker compose -f deploy/docker-compose/docker-compose.yaml --profile interactive run orchestrator
```

For the latter, there's a small helper `startall.sh` script that brings up all the agent Go binaries in the background including the optional Gateway and the interfactive aiHelpDesk session too:

```
  ./startall.sh
```

Please be sure to review this helper script and set your desired LLM model for aiHelpDesk (it defaults to Anthropic's Haiku) and your API key.

See [VM-based Deployment](deploy/docker-compose/README.md) for detailed instructions on how to deploy aiHelpDesk either as binaries (simpler) or manually by cloning the repo.

## Architecture
See aiHelpDesk's [ARCHITECTURE.md](ARCHITECTURE.md) for system design, configuration, API reference, and extension guide.

## Testing
See [testing/README.md](testing/README.md) for the failure injection testing framework included with aiHelpDesk.

## Demo app calling aiHelpDesk
aiHelpDesk can certainly be used by humans and that's what the LLM-powered orchestrator is there for. Additionally however, an upstream agent or a program can call aiHelpDesk agents directly.
See [cmd/srebot/README.md](cmd/srebot/README.md) for a sample of a O11y watcher program or an SRE bot that calls aiHelpDesk (via a gateway) to understand a state of a database and ask for AI-powered diagnostic and troubleshooting.
