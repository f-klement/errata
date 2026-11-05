# Operations

The API listens on port <!-- errata: api-port-agrees -->8080<!-- /errata -->. Open it on the
load balancer and nowhere else.

| Service | Replicas | Port |
|---|---|---|
| api | 1 | 8080 |
| web | 3 | 8080 |
| worker | 1 | none |

## Routes

The API serves `/items` and `/shelves`. Each route is listed here, so the support team knows
what a customer can call.

## Restore

```bash
larder restore --from /var/backups/larder
```
