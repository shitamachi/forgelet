const fs = require("fs");
if (!process.version.startsWith("v24.")) process.exit(24);
fs.appendFileSync(process.env.GITHUB_OUTPUT, `answer=${process.env.INPUT_VALUE}\n`);
fs.appendFileSync(process.env.GITHUB_STATE, "done=yes\n");
console.log(`node24-runtime=${process.version}`);
