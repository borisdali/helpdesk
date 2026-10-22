# aiHelpDesk Sample#25 (on K8s): Proof Before Overwrite

The raw transcript of the sample commands and deliberations presented below complements this blog post:

- **[Postgres Never Asks for RESETLOGS](https://itnext.io/postgres-never-asks-for-resetlogs-d313f6801ac5)**  
  A restore with a hole in its history looks just like one without. aiHelpDesk proves the difference before it overwrites anything
  
  
If you are new to aiHelpDesk, start with the Customer [Bill of Rights](../CUSTOMER_RIGHTS.md). 10 specific entitlements. Verifiable on a live system. Your system.
Next, review another aiHelpDesk pioneering concept: the [Operational SRE/DBA Flywheel](../VAULT.md#the-operational-sredba-flywheel).

How do the former (rights/entitlements) map to the latter (the flywheel, which is essentially a loop)? Chek out [this doc page](../RIGHTS_AND_THE_FLYWHEEL.md).

Finally, take aiHelpDesk for a spin! Here's a link to the 10-minute demo: [this page](../../deploy/docker-compose/DEMO.md).

---

As with all sample pages, each one is using the syntax from one of the supported platforms: running commands from the source code, on VM/Bare Metal, on Docker/Podman or on K8s. This sample transcript was created entirely on K8s. Not because the capabilities shown here are new or special (all three were already built and live-verified against Docker), but because K8s is where every one of the real bugs in this transcript was actually found. See [here](SAMPLE010.md), [here](SAMPLE021.md) and [here](SAMPLE019.md) for VM/Bare Metal, the source and container (Docker/Podman) sample transcripts respectively.

This sample transcript is meant to showcase the new capabilities introduced in the [v0.32 release](https://github.com/borisdali/helpdesk/releases/tag/v0.32.0). The samples are of real runs against the live K8s cluster. Not `helm template`, not a unit test, not a mock:

---

## 


