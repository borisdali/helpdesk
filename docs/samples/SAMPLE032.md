# aiHelpDesk Sample#32 (Docker): Drilled Recovery.

The raw transcript of the sample commands and deliberations presented below complements this blog post:

- **[Your AI Won’t Run Your Fire Drills, but It Will Check That You Did](https://itnext.io/your-ai-wont-run-your-fire-drills-but-it-will-check-that-you-did-c20156397868)**  
  A backup nobody has restored is a hope. Restore nobody has practiced is a first attempt on the worst day… billed in downtime against a budget you never measured


If you are new to aiHelpDesk, start with the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md). 10 specific entitlements. Verifiable on a live system. Your system.
Next, review another aiHelpDesk pioneering concept: the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel).

How do the former (rights/entitlements) map to the latter (the flywheel, which is essentially a loop)? Chek out [this doc page](../RIGHTS_AND_THE_FLYWHEEL.md).

Finally, take aiHelpDesk for a spin! Here's a link to the 10-minute demo: [this page](../../deploy/docker-compose/DEMO.md).

---

As with all sample pages, each one is using the syntax from one of the supported platforms: running commands from the source code, on VM/Bare Metal, on Docker/Podman or on K8s. This sample transcript was created entirely for Docker, but see [here](SAMPLE027.md), [here](SAMPLE021.md) and [here](SAMPLE028.md) for K8s, the source and host/VM sample transcripts respectively.

In more general terms [docs/PLATFORMS.md](../PLATFORMS.md) is the authoritative source for the supported platforms: where aiHelpDesk auditd, audit Postgres backend store, agents, pgBackRest repo, Gateway and the target database can run).

---

The goal of this transcript is simple: give a taste of the actual commands behind [aiHelpDesk v0.36](https://github.com/borisdali/helpdesk/releases/tag/v0.36.0) release: ...

See [the ops guide](../RESTORE_OPS.md#11-set-the-objectives) and [the previous transcript](SAMPLE030.md#1-the-declaration-infrajson-inventory) for how to configure objectives, RPO, RTO and availability SLO.




## 

