# aiHelpDesk: Deployment for K8s

## 1. Deployment from binary

```
  tar xzf helpdesk-v1.0.0-deploy.tar.gz
  kubectl create secret generic helpdesk-api-key --from-literal=api-key=<YOUR_API_KEY>
  helm install helpdesk helpdesk-v1.0.0-deploy/helm/helpdesk \
    --set model.vendor=anthropic \
    --set model.name=claude-haiku-4-5-20251001
```

TODO(bdali): expand...
