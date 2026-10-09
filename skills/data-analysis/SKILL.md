---
name: data-analysis
description: Analyze data, process CSV/JSON files, compute statistics, and create data visualizations. Use when the user asks about data processing, statistics, or analysis.
metadata:
  fastagent:
    always: false
---

# Data Analysis Skill

Analyze and process data using Python in the sandbox.

## Common Libraries
- **pandas**: DataFrames, CSV/JSON/Excel processing
- **numpy**: Numerical computing
- **matplotlib**: Visualization (use Agg backend)

Install if needed: `pip install pandas numpy matplotlib`

## Common Tasks

### Read and analyze CSV
```python
import pandas as pd
df = pd.read_csv('data.csv')
print(df.describe())
print(f"\nShape: {df.shape}")
print(f"\nColumns: {list(df.columns)}")
print(f"\nFirst 5 rows:\n{df.head()}")
```

### Create a chart from data
Save the chart as a real file. Reference the file with a relative path.
```python
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
import pandas as pd

df = pd.read_csv('data.csv')
df.plot(kind='bar', x='category', y='value', figsize=(10, 6))
plt.title('Data Overview')
plt.tight_layout()
plt.savefig('chart.png', dpi=150)
print('Wrote chart.png')
```
Point the report at the file:
```markdown
![Data overview](chart.png)
```

### JSON processing
```python
import json
with open('data.json') as f:
    data = json.load(f)
# Process and analyze...
```

## Guidelines
- Run the analysis and show the result. Do not show code alone.
- Show these statistics: shape, dtypes, describe(), and null counts.
- For a large dataset, show head, tail, and summary statistics.
- Make a chart when a chart explains the data.
- Save each chart as a real file in the workspace. Use a relative path, for example `chart.png`.
- Reference a chart with a relative path, for example `![chart](chart.png)`. In HTML, use `<img src="chart.png">`.
- Put the chart file in the same directory as the report. The reader resolves a relative path against the report directory.
- Do not put base64 image data in the report. One chart can add hundreds of kilobytes. The reader must decode that full string before the page shows.
