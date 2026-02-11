# Vector Agent Examples for Xray Nodes

This directory contains example configurations for deploying **Vector** log collector on Xray nodes.

## Files

- **`vector-node.toml`** — Vector configuration for parsing Xray access logs and sending to Observer
- **`docker-compose.node.yml`** — Docker Compose file for deploying Vector as a container

## Quick Start

1. Copy files to your node:
   ```bash
   scp vector-node.toml docker-compose.node.yml node:/opt/vector-agent/
   ```

2. Customize `vector-node.toml`:
   - Update log path (`include = ["/var/log/xray/access.log"]`)
   - Adjust regex to match your Xray log format
   - Change Observer URL if needed

3. Deploy:
   ```bash
   cd /opt/vector-agent
   docker-compose -f docker-compose.node.yml up -d
   ```

4. Verify:
   ```bash
   docker logs -f vector-agent
   ```

## Full Documentation

See [Observer docs/VECTOR-AGENT-SETUP.md](../../observer/docs/VECTOR-AGENT-SETUP.md) for:
- Detailed setup instructions
- Log format examples and regex patterns
- Troubleshooting guide
- Performance tuning
- Monitoring and health checks

## Why Vector Instead of Blocker?

After **MIG-10** migration:
- ❌ **Old:** RabbitMQ + Blocker (IP-level blocking via nftables)
- ✅ **New:** Vector + Observer (user-level enforcement via Remnawave API)

**Benefits:**
- Simpler architecture (no message broker)
- More effective (users can't bypass by changing IP)
- Automatic re-enable (scheduler-based)
- Lower resource usage (<10 MB RAM vs ~50 MB for Blocker)

See [ADR-007](../../observer/docs/adr/ADR-007-remnawave-user-enforcement.md) for architecture details.
