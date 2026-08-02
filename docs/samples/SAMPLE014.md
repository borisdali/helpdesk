# aiHelpDesk Sample#14 (on Docker): Demo on legacy i386 platforms

Please skip this sample page if you run on modern 64-bit platforms. But if you have the old 32-bit and it's not capable of running 64-bit apps natively, consider this walkaround of using the QEMU emulator.

First things first though, [here]() is the official doc for our 10 minutes demo and [here](SAMPLE013.md)'s the sample commands that show what comes out when you run it.

If i386 is your only option, these notes may help (but also please reach out to us and we can help with the private i386 image):

```
boris@ ~/helpdesk/deploy/docker-compose$ sudo apt install qemu-user-static binfmt-support

boris@ ~/helpdesk/deploy/docker-compose$ docker-compose -f docker-compose.demo.yaml ps
         Name                       Command                       State                            Ports
--------------------------------------------------------------------------------------------------------------------------
helpdesk-demo-auditd     /bin/sh -c sleep 3 && exec ...   Up (health: starting)
helpdesk-demo-postgres   docker-entrypoint.sh postg ...   Up (healthy)            0.0.0.0:5434->5432/tcp,:::5434->5432/tcp

boris@ ~/helpdesk/deploy/docker-compose$ docker-compose -f docker-compose.demo.yaml logs demo-auditd
Attaching to helpdesk-demo-auditd
helpdesk-demo-auditd | exec /bin/sh: exec format error
helpdesk-demo-auditd | exec /bin/sh: exec format error

boris@ ~/helpdesk/deploy/docker-compose$ docker run --privileged --rm tonistiigi/binfmt --install linux/amd64
boris@ ~/helpdesk/deploy/docker-compose$ sudo update-binfmts --display qemu-x86_64
boris@ ~/helpdesk/deploy/docker-compose$ sudo dpkg -L qemu-user-static | grep -i binfmt

boris@ ~/helpdesk/deploy/docker-compose$ sudo cat > /usr/share/binfmts/qemu-x86_64 <<'EOF'
package qemu-user-static
interpreter /usr/bin/qemu-x86_64-static
magic \x7f\x45\x4c\x46\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00
offset 0
mask \xff\xff\xff\xff\xff\xff\xff\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff
credentials no
fix_binary yes
preserve yes
EOF

boris@ ~/helpdesk/deploy/docker-compose$ sudo update-binfmts --importdir /usr/share/binfmts --import qemu-x86_64
boris@ ~/helpdesk/deploy/docker-compose$ sudo update-binfmts --display qemu-x86_64
qemu-x86_64 (enabled):
     package = qemu-user-static
        type = magic
      offset = 0
       magic = \x7f\x45\x4c\x46\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00
        mask = \xff\xff\xff\xff\xff\xff\xff\x00\xff\xff\xff\xff\xff\xff\xff\xff\xfe\xff\xff\xff
 interpreter = /usr/bin/qemu-x86_64-static
    detector =

boris@ ~/helpdesk/deploy/docker-compose$ docker run --rm --platform linux/amd64 debian:bookworm-slim uname -m
x86_64

boris@ ~/helpdesk/deploy/docker-compose$ docker run --rm --platform linux/amd64 debian:bookworm-slim uname -m
x86_64

boris@ ~/helpdesk/deploy/docker-compose$ docker-compose -f docker-compose.demo.yaml up -d
helpdesk-demo-auditd is up-to-date
helpdesk-demo-postgres is up-to-date
Creating helpdesk-demo-db-agent ... done
Creating helpdesk-demo-gateway  ... done
Creating helpdesk-demo-runner   ... done

boris@ ~/helpdesk/deploy/docker-compose$ docker-compose -f docker-compose.demo.yaml ps
         Name                       Command                  State                        Ports
-----------------------------------------------------------------------------------------------------------------
helpdesk-demo-auditd     /bin/sh -c sleep 3 && exec ...   Up (healthy)
helpdesk-demo-db-agent   /usr/local/bin/database-agent    Up
helpdesk-demo-gateway    /usr/local/bin/gateway           Up (healthy)   0.0.0.0:8180->8180/tcp,:::8180->8180/tcp
helpdesk-demo-postgres   docker-entrypoint.sh postg ...   Up (healthy)   0.0.0.0:5434->5432/tcp,:::5434->5432/tcp
helpdesk-demo-runner     /bin/bash /demo/run.sh           Up

boris@ ~/helpdesk/deploy/docker-compose$ docker-compose -f docker-compose.demo.yaml run --rm demo-runner
Creating docker-compose_demo-runner_run ... done
────────────────────────────────────────────────────────────────
  aiHelpDesk — Governed AI Incident Response Demo
────────────────────────────────────────────────────────────────

  Fault:     Max connections exhausted (db-max-connections)
  Mode:      interactive approval (mode B)
  Playbook:  pbs_connection_remediate
  Model:     anthropic / claude-haiku-4-5-20251001
  Gateway:   http://demo-gateway:8180
  Rights:    I (Consent) · III (Audit Trail) · IV (Grade) · IX (Original Claim)

────────────────────────────────────────────────────────────────

▶ Step 1/5 — Waiting for services to be ready...
  Waiting for demo-postgres
✓ demo-postgres is ready
  Waiting for gateway
✓ gateway is ready

────────────────────────────────────────────────────────────────
▶ Step 2/5 — Injecting fault...
▶ Injecting fault: saturating connection pool with idle sessions...
▶   max_connections=30, superuser_reserved=3, opening 20 idle connections...
✓ Fault active: 20 idle connections holding the pool (max=30)

▶   Current database state:
 total_connections | idle | active
-------------------+------+--------
                26 |   20 |      1
(1 row)


────────────────────────────────────────────────────────────────
▶ Step 3/5 — Triggering playbook 'pbs_connection_remediate'...
▶   The step proposer is planning the first remediation action (15–45 seconds)...
...
```
