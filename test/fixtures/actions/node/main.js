const fs = require('fs');
if (process.env.STATE_pre !== process.env.INPUT_VALUE) process.exit(21);
fs.appendFileSync(process.env.GITHUB_OUTPUT, `answer=${process.env.INPUT_VALUE}\n`);
fs.appendFileSync(process.env.GITHUB_STATE, 'main=ran\n');
fs.appendFileSync(process.env.GITHUB_ENV, 'SHARED=from-action\n');
console.log(`main-${process.env.INPUT_VALUE}`);
if (process.env.INPUT_FAIL === 'true') process.exit(7);
