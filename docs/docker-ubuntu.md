# PDH Server on Ubuntu with Docker

The installer targets Ubuntu Linux and installs Docker Engine and the Compose plugin from Ubuntu packages. It builds the application into an Ubuntu 24.04 image and runs PostgreSQL separately.

## Install

From a clone of this repository on the Ubuntu host:

```bash
chmod +x install-docker.sh
./install-docker.sh
```

The script may prompt for sudo access. On its first run it generates database, JWT, and updater secrets in `.env.docker` and restricts that file to the current user. Keep this file private and backed up. It also builds and starts the isolated updater service; the web application does not receive the Docker socket. The application applies pending database migrations when it starts.

After the containers become healthy, open `http://<server-ip>:8090`. The PostgreSQL port is not published on the host. For internet-facing use, put TLS termination and a reverse proxy in front of PDH, and restrict direct access to port 8090 with the host firewall.

## Operations

Administrators can check for updates and install them from **Core-Einstellungen**. The updater fetches only the configured GitHub `main` branch, refuses local tracked changes and non-fast-forward updates, builds the app and updater images, and restarts those services. Automatic checks only report availability; they never install without an administrator action.

The updater container mounts the Docker socket and the repository checkout because it must rebuild/restart services. It is not exposed on a host port and only accepts requests authenticated with the private token in `.env.docker`. Treat the host and this token as privileged. If a build fails, the existing app image remains in service and the admin can retry; the fetched checkout is retained for that retry. To update manually instead, run the installer again; it preserves secrets and named volumes.

Stop the services without deleting data:

```bash
sudo docker compose --env-file .env.docker down
```

Delete the database and uploaded files only when intentionally removing all persistent data:

```bash
sudo docker compose --env-file .env.docker down --volumes
```

The Compose project uses the `pdh_database` and `pdh_uploads` named volumes. Back them up according to your normal Docker/PostgreSQL backup policy before upgrades or host maintenance.
