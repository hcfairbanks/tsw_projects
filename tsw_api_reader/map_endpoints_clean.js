const axios = require('axios');
const fs = require('fs');
const path = require('path');

// Top Level Nodes
const TOP_LEVEL_NODES = [
    'CurrentDrivableActor',
    'CurrentFormation',
    'DriverAid',
    'DriverInput',
    'Player',
    'TimeOfDay',
    'VirtualRailDriver',
    'WeatherManager'
];

// const TOP_LEVEL_NODES = [
//     'DriverAid',
//     'TimeOfDay',
//     'WeatherManager'
// ];

// Configuration
const BASE_URL = 'http://localhost:31270';
const windows_users_folder = process.env.USERPROFILE || 'DefaultUser';
const apiKeyPath = path.join(windows_users_folder, 'Documents', 'My Games', 'TrainSimWorld6', 'Saved', 'Config', 'CommAPIKey.txt');
const MAX_DEPTH = 20;
const DELAY_MS = 250; // 1/4 second

// Read API key
let apiKey;
try {
    apiKey = fs.readFileSync(apiKeyPath, 'utf8').trim();
    console.log('✓ API Key loaded successfully\n');
} catch (err) {
    console.error('✗ Failed to read API key:', err.message);
    process.exit(1);
}

// Store discovered GET endpoints with their data
const discoveredEndpoints = [];
let requestCount = 0;
let maxDepthAchieved = 0;

// Track completed paths to support resume
const completedPaths = new Set();

// Output file path (initialized in main)
let outputFilePath = null;
let results = null;

/**
 * Write current results to file
 */
function writeResultsToFile(isCompleted = false) {
    if (!outputFilePath || !results) return;
    
    results.completed = isCompleted;
    results.totalRequests = requestCount;
    results.totalEndpoints = discoveredEndpoints.length;
    results.maxDepthAchieved = maxDepthAchieved;
    results.endpoints = discoveredEndpoints;
    results.completedPaths = Array.from(completedPaths);
    
    fs.writeFileSync(outputFilePath, JSON.stringify(results, null, 2));
}

/**
 * Load existing results if available
 * Returns: 'completed' if node is fully done, 'resumed' if partially done, false if new
 */
function loadExistingResults() {
    if (!outputFilePath || !fs.existsSync(outputFilePath)) {
        return false;
    }
    
    try {
        const existingData = JSON.parse(fs.readFileSync(outputFilePath, 'utf8'));
        
        // Check if this node was already completed
        if (existingData.completed === true) {
            console.log(`✓ Node already completed, skipping...\n`);
            return 'completed';
        }
        
        // Restore discovered endpoints
        if (existingData.endpoints && Array.isArray(existingData.endpoints)) {
            discoveredEndpoints.push(...existingData.endpoints);
        }
        
        // Restore completed paths
        if (existingData.completedPaths && Array.isArray(existingData.completedPaths)) {
            existingData.completedPaths.forEach(path => completedPaths.add(path));
        }
        
        // Restore counters
        requestCount = existingData.totalRequests || 0;
        maxDepthAchieved = existingData.maxDepthAchieved || 0;
        
        console.log(`✓ Resumed from existing file: ${discoveredEndpoints.length} endpoints, ${completedPaths.size} completed paths\n`);
        return 'resumed';
    } catch (err) {
        console.log(`⚠ Could not load existing results: ${err.message}`);
        return false;
    }
}

/**
 * Sleep function
 */
function sleep(ms) {
    return new Promise(resolve => setTimeout(resolve, ms));
}

/**
 * Make API request
 */
async function apiRequest(url) {
    try {
        requestCount++;
        const response = await axios.get(url, {
            headers: { 'DTGCommKey': apiKey },
            timeout: 5000
        });
        await sleep(DELAY_MS);
        return response.data;
    } catch (err) {
        console.error(`✗ Error: ${url} - ${err.message}`);
        return null;
    }
}

/**
 * Explore a path recursively
 */
async function explorePath(pathSegments, depth = 0) {
    if (depth > MAX_DEPTH) {
        return;
    }
    
    const currentPath = pathSegments.join('/');
    
    // Skip if this path was already completed
    if (completedPaths.has(currentPath)) {
        console.log(`${'  '.repeat(depth)}⊙ Skipping completed: ${currentPath}`);
        return;
    }
    
    // Track maximum depth achieved
    if (depth > maxDepthAchieved) {
        maxDepthAchieved = depth;
    }

    const indent = '  '.repeat(depth);
    const listUrl = `${BASE_URL}/list/${currentPath}`;
    
    console.log(`${indent}→ Listing: ${currentPath}`);
    
    const data = await apiRequest(listUrl);
    
    if (!data || data.Result !== 'Success') {
        console.log(`${indent}  ✗ Failed`);
        return;
    }

    // Process Endpoints (these are actual data endpoints we want to record)
    if (data.Endpoints && data.Endpoints.length > 0) {
        console.log(`${indent}  Found ${data.Endpoints.length} endpoints`);
        
        for (const endpoint of data.Endpoints) {
            const endpointPath = `${currentPath}.${endpoint.Name}`;
            const getUrl = `${BASE_URL}/get/${endpointPath}`;
            
            console.log(`${indent}    Testing: ${endpoint.Name}`);
            
            const endpointData = await apiRequest(getUrl);
            
            if (endpointData && endpointData.Result === 'Success') {
                discoveredEndpoints.push({
                    url: getUrl,
                    data: endpointData
                });
                console.log(`${indent}      ✓ Recorded`);
                
                // Write to file after each endpoint is discovered
                writeResultsToFile();
            }
        }
    }

    // Process Nodes (these are child paths to explore)
    if (data.Nodes && data.Nodes.length > 0) {
        console.log(`${indent}  Found ${data.Nodes.length} child nodes`);
        
        for (const node of data.Nodes) {
            const newPath = [...pathSegments, node.Name];
            await explorePath(newPath, depth + 1);
        }
    }
    
    // Mark this path as completed after exploring all endpoints and child nodes
    completedPaths.add(currentPath);
    writeResultsToFile();
}

/**
 * Process a single node
 */
async function processNode(targetNode, nodeIndex, totalNodes) {
    console.log('====================================');
    console.log(`[${nodeIndex}/${totalNodes}] ${targetNode} Endpoint Mapper`);
    console.log('====================================');
    console.log('Base URL:', BASE_URL);
    console.log(`Max Depth: ${MAX_DEPTH}`);
    console.log(`Delay: ${DELAY_MS}ms`);
    console.log('====================================\n');
    
    const startTime = Date.now();
    
    // Reset state for this node
    discoveredEndpoints.length = 0;
    completedPaths.clear();
    requestCount = 0;
    maxDepthAchieved = 0;
    
    // Initialize results structure and output file
    const outputDir = path.join(__dirname, 'endpoints');
    if (!fs.existsSync(outputDir)) {
        fs.mkdirSync(outputDir, { recursive: true });
    }
    
    outputFilePath = path.join(outputDir, `${targetNode}_endpoints.json`);
    
    // Try to load existing results for resume capability
    const loadStatus = loadExistingResults();
    
    // Skip if already completed
    if (loadStatus === 'completed') {
        return { skipped: true, targetNode };
    }
    
    // Initialize results structure (or update existing)
    results = {
        completed: false,
        baseUrl: BASE_URL,
        targetNode: targetNode,
        discoveredAt: loadStatus === 'resumed' ? results?.discoveredAt || new Date().toISOString() : new Date().toISOString(),
        maxDepth: MAX_DEPTH,
        maxDepthAchieved: 0,
        totalRequests: 0,
        totalEndpoints: 0,
        endpoints: [],
        completedPaths: []
    };
    
    if (!loadStatus) {
        // Write initial empty structure only if not resuming
        writeResultsToFile(false);
        console.log(`✓ Output file initialized: ${outputFilePath}\n`);
    }
    
    // Start exploration
    await explorePath([targetNode]);
    
    const endTime = Date.now();
    const duration = ((endTime - startTime) / 1000).toFixed(2);
    
    console.log('\n====================================');
    console.log('✓✓✓ EXPLORATION COMPLETE! ✓✓✓');
    console.log('====================================');
    console.log(`Total API requests: ${requestCount}`);
    console.log(`Total GET endpoints recorded: ${discoveredEndpoints.length}`);
    console.log(`Total paths explored: ${completedPaths.size}`);
    console.log(`Time taken: ${duration}s`);
    console.log('====================================\n');
    
    // Final write with completed flag set to true
    writeResultsToFile(true);
    console.log(`✓ Final results saved to: ${outputFilePath}\n`);
    
    // Format runtime
    const totalSeconds = Math.floor((endTime - startTime) / 1000);
    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const seconds = totalSeconds % 60;
    
    let runtimeFormatted;
    if (hours > 0) {
        runtimeFormatted = `${hours} hours : ${minutes} minutes : ${seconds} seconds`;
    } else {
        runtimeFormatted = `${minutes} minutes : ${seconds} seconds`;
    }
    
    // Generate report
    const now = new Date();
    const dateStr = now.toISOString().split('T')[0].replace(/-/g, ':');
    const timeStr = now.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', second: '2-digit', timeZoneName: 'short' });
    
    const report = `Date: ${dateStr}
Time: ${timeStr}
TargetNode: ${results.targetNode}
MaxDepthAchieved: ${maxDepthAchieved}
Runtime: ${runtimeFormatted}
OutputFile: ${path.basename(outputFilePath)}`;
    
    // Create timestamp for report filename (YYYYMMDD_HHMMSS)
    const timestamp = now.toISOString().replace(/[-:]/g, '').replace('T', '_').split('.')[0];
    const reportsDir = path.join(__dirname, 'reports');
    
    // Create reports directory if it doesn't exist
    if (!fs.existsSync(reportsDir)) {
        fs.mkdirSync(reportsDir, { recursive: true });
    }
    
    const reportFile = path.join(reportsDir, `report_${results.targetNode}_${timestamp}.txt`);
    fs.writeFileSync(reportFile, report);
    console.log(`✓ Report saved to: ${reportFile}\n`);
    
    console.log('====================================');
    console.log('Sample Endpoints:');
    console.log('====================================');
    discoveredEndpoints.slice(0, 10).forEach(ep => {
        console.log(`${ep.url}`);
    });
    if (discoveredEndpoints.length > 10) {
        console.log(`... and ${discoveredEndpoints.length - 10} more`);
    }
    
    console.log('\n====================================');
    console.log(`${targetNode} FINISHED SUCCESSFULLY`);
    console.log('====================================\n');
    
    return { skipped: false, targetNode, endpointCount: discoveredEndpoints.length };
}

/**
 * Main function - processes all nodes
 */
async function main() {
    console.log('\n');
    console.log('########################################');
    console.log('#   TSW5 API ENDPOINT MAPPER - BATCH   #');
    console.log('########################################');
    console.log(`Processing ${TOP_LEVEL_NODES.length} top-level nodes...\n`);
    
    const overallStartTime = Date.now();
    const results = [];
    
    for (let i = 0; i < TOP_LEVEL_NODES.length; i++) {
        const node = TOP_LEVEL_NODES[i];
        const result = await processNode(node, i + 1, TOP_LEVEL_NODES.length);
        results.push(result);
    }
    
    const overallEndTime = Date.now();
    const totalDuration = ((overallEndTime - overallStartTime) / 1000).toFixed(2);
    
    // Summary
    console.log('\n');
    console.log('########################################');
    console.log('#         ALL NODES COMPLETE!          #');
    console.log('########################################');
    console.log(`Total time: ${totalDuration}s\n`);
    
    console.log('Summary:');
    results.forEach(r => {
        if (r.skipped) {
            console.log(`  ⊙ ${r.targetNode}: Skipped (already completed)`);
        } else {
            console.log(`  ✓ ${r.targetNode}: ${r.endpointCount} endpoints`);
        }
    });
    
    console.log('\n########################################');
    console.log('#    SCRIPT FINISHED SUCCESSFULLY      #');
    console.log('########################################\n');
}

// Run
main().catch(err => {
    console.error('\n✗✗✗ FATAL ERROR ✗✗✗');
    console.error(err);
    process.exit(1);
}).finally(() => {
    process.exit(0);
});
