if (process.env.STATE_main !== 'ran') process.exit(22);
console.log(`post-${process.env.INPUT_VALUE}`);
