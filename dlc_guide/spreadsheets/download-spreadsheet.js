const https = require("https");
const fs = require("fs");
const path = require("path");

const SPREADSHEET_ID = "1fVexHITBCNJnjrDROezcr-2TI4EKI8KnSrIwpFHQ2AE";
const GID = "0";

const outputFile = process.argv[2] || "spreadsheet_data.csv";
const outputPath = path.resolve(outputFile);

const url = `https://docs.google.com/spreadsheets/d/${SPREADSHEET_ID}/gviz/tq?tqx=out:csv&gid=${GID}`;

console.log(`Downloading spreadsheet...`);
console.log(`URL: ${url}`);

function fetch(url, redirects = 0) {
  if (redirects > 5) {
    console.error("Too many redirects");
    process.exit(1);
  }

  https.get(url, (res) => {
    if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
      console.log(`Redirecting to: ${res.headers.location}`);
      fetch(res.headers.location, redirects + 1);
      return;
    }

    if (res.statusCode !== 200) {
      console.error(`Failed with status ${res.statusCode}`);
      process.exit(1);
    }

    const file = fs.createWriteStream(outputPath);
    res.pipe(file);
    file.on("finish", () => {
      file.close();
      const stats = fs.statSync(outputPath);
      console.log(`Saved to: ${outputPath}`);
      console.log(`Size: ${(stats.size / 1024).toFixed(1)} KB`);
    });
  }).on("error", (err) => {
    console.error(`Error: ${err.message}`);
    process.exit(1);
  });
}

fetch(url);
