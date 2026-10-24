const fs = require('fs');
fs.appendFileSync(process.env.GITHUB_STATE, `pre=${process.env.INPUT_VALUE}\n`);
console.log(`pre-${process.env.INPUT_VALUE}`);
