# aiHelpDesk: AI DB SRE in a box: a Multi-Agent Support System

An Go-based AI multi-agent self-service help/support system for troubleshooting PostgreSQL databases hosted on Kubernetes or VM infrastructure, with incident diagnostic bundle management for vendor support and a built-in failure testing injection framework. aiHelpDesk is implemented using Google ADK and the A2A (Agent-to-Agent) protocol for extensibility where agents can be added or replaced in addition to those provided with aiHelpDesk out of the box.

aiHepDesk is designed to help customers with AI-assisted triage, root cause analysis and remediation of database related problems on K8s and VMs.

## Architecture
See aiHelpDesk's [ARCHITECTURE.md](ARCHITECTURE.md) for system design, configuration, API reference, and extension guide.

## Testing
See [testing/README.md](testing/README.md) for the failure injection testing framework included with aiHelpDesk.

## Testing
See [cmd/srebot/README.md](cmd/srebot/README.md) for a sample of a O11y watcher program or an SRE bot that calls aiHelpDesk (via a gateway) to understand a state of a database and ask for AI-powered diagnostic and troubleshooting.
