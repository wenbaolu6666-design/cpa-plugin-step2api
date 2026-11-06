const fs = require("fs");
const path = require("path");
const base = "D:/WenPaul/Documents/路由网关/cpa-plugin-step2api";
function put(rel, enc) {
  const p = path.join(base, rel);
  fs.mkdirSync(path.dirname(p), { recursive: true });
  fs.writeFileSync(p, decodeURIComponent(enc).trim() + "\n", "utf8");
  console.log("Wrote:", rel);
}
module.exports = { put };
