# aiHelpDesk: AI DB SRE in a box: a Multi-Agent Support System
A Go-based AI multi-agent self-service help/support system for troubleshooting PostgreSQL databases hosted on Kubernetes or VM infrastructure, with incident diagnostic bundle management for vendor support and a built-in failure testing injection framework. aiHelpDesk represents the shift-left support paradigm and is implemented using Google ADK and the A2A (Agent-to-Agent) protocol for extensibility where agents can be added or replaced in addition to those provided with aiHelpDesk out of the box.

aiHepDesk is designed to help customers with AI-assisted triage, root cause analysis and remediation of database related problems on K8s and VMs.

## AI-Assisted Database Management
While SaaS applications clearly have its market and cloud vendor's DBaaS systems in particular are among the fastest growing and most profitable sectors for GCP, AWS and Azure, there are legitimate reasons whey many customers prefer to stay away from the black-box, vendor lock-in  approach. See [here](https://medium.com/google-cloud/databases-on-k8s-really-part-8-182259e1720f) for extensive treatment of this topic, the customer expectations of the cloud provider's DBaaS and how they often fall short. Enter the AI-Assisted Database Management products.

aiHelpDesk is the first accompanying product that starts on the path of implementing the AI-Assisted promise of this new breed of the database management products.

## Deployment
aiHelpDesk can be deployed on K8s or VMs depending on where you run your databases.

  ### On K8s

  ### on VMs
See [VM-based Deployment](deploy/docker-compose/README.md) for instructions on how to deploy aiHepDesk either as a binary (simpler) or manually by cloning the repo.

## Architecture
See aiHelpDesk's [ARCHITECTURE.md](ARCHITECTURE.md) for system design, configuration, API reference, and extension guide.

## Testing
See [testing/README.md](testing/README.md) for the failure injection testing framework included with aiHelpDesk.

## Demo app calling aiHelpDesk
aiHelpDesk can certainly be used by humans and that's what the LLM-powered orchestrator is there for. Additionally however, an upstream agent or a program can call aiHelpDesk agents directly.
See [cmd/srebot/README.md](cmd/srebot/README.md) for a sample of a O11y watcher program or an SRE bot that calls aiHelpDesk (via a gateway) to understand a state of a database and ask for AI-powered diagnostic and troubleshooting.
