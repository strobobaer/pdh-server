# PDH Server on Ubuntu with Docker

The installer targets Ubuntu Linux and installs Docker Engine and the Compose plugin from Ubuntu packages. It builds the application into an Ubuntu 24.04 image and runs PostgreSQL separately.

## Install

From a clone of this repository on the Ubuntu host:

```bash
chmod +x install-docker.sh
./install-docker.sh
```

The script may prompt for sudo access. On its first run it generates a database password and JWT signing secret in `.env.docker` and restricts that file to the current user. Keep this file private and backed up. The application applies pending database migrations when it starts.

After the containers become healthy, open `http://<server-ip>:8090`. The PostgreSQL port is not published on the host. For internet-facing use, put TLS termination and a reverse proxy in front of PDH, and restrict direct access to port 8090 with the host firewall.

## Operations

Run the installer again from the repository after updating the source to rebuild and restart PDH. It preserves `.env.docker` and the named volumes.

Stop the services without deleting data:

```bash
sudo docker compose --env-file .env.docker down
```

Delete the database and uploaded files only when intentionally removing all persistent data:

```bash
sudo docker compose --env-file .env.docker down --volumes
```

The Compose project uses the `pdh_database` and `pdh_uploads` named volumes. Back them up according to your normal Docker/PostgreSQL backup policy before upgrades or host maintenance.
