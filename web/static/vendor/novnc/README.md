# noVNC 1.6.0 (gebündelt)

`rfb.min.js` ist der VNC-Client [noVNC](https://github.com/novnc/noVNC) 1.6.0
(MPL-2.0, Lizenzen siehe `LICENSE.txt`) als ein einziges ES-Modul, damit der
HMI-Fernzugriff auch ohne Internet funktioniert. Export: `RFB`.

Neu bauen (aus den ES-Modul-Quellen `core/`, nicht aus dem npm-Paket `lib/`):

```sh
curl -sL https://github.com/novnc/noVNC/archive/refs/tags/v1.6.0.tar.gz | tar xz
echo "export { default as RFB } from './noVNC-1.6.0/core/rfb.js';" > entry.js
npx esbuild entry.js --bundle --minify --format=esm --target=es2022 --legal-comments=eof --outfile=rfb.min.js
```
