import pandas as pd
import matplotlib.pyplot as plt
import seaborn as sns

def analyze_rtt(filename="rtt_measurements.csv"):
    """
    Reads RTT data from a CSV file, calculates statistics,
    and generates a combined box and violin plot.
    """
    try:
        df = pd.read_csv(filename)
    except FileNotFoundError:
        print(f"Error: The file '{filename}' was not found.")
        print("Please run the Go program first to generate the data.")
        return

    # --- Data Cleaning and Preparation ---
    # Remove unrealistic RTT values
    df = df[df["rtt_ms"] < 4000]
    if df.empty:
        print("No valid data to plot. The CSV might be empty or contain only outliers.")
        return

    # --- Statistical Summary ---
    print("--- RTT Statistics Summary ---")
    summary = df.groupby(["device_label", "screen_state"])["rtt_ms"].agg(['count', 'mean', 'median', 'std', 'min', 'max'])
    print(summary)
    print("-------------------------------")

    # --- Plotting ---
    plt.style.use('seaborn-v0_8-whitegrid')
    fig, ax = plt.subplots(figsize=(10, 7))

    # Create a combined 'category' for plotting
    df['category'] = df['device_label'] + "\n" + df['screen_state']

    # Order for plotting
    order = sorted(df['category'].unique())

    # Box plot
    sns.boxplot(data=df, x='category', y='rtt_ms', order=order, ax=ax,
                showfliers=False,  # Outliers are often distracting
                boxprops=dict(alpha=.7))

    # Violin plot
    sns.violinplot(data=df, x='category', y='rtt_ms', order=order, ax=ax,
                   inner=None, color=".8", cut=0)

    # --- Aesthetics and Labels ---
    ax.set_title('RTT Distribution by Platform and Screen State', fontsize=16)
    ax.set_xlabel('Device and State', fontsize=12)
    ax.set_ylabel('Round-Trip Time (ms)', fontsize=12)
    plt.xticks(rotation=0)
    plt.tight_layout()

    # --- Save and Show Plot ---
    output_filename = 'rtt_analysis_plot.png'
    plt.savefig(output_filename)
    print(f"\nPlot saved as '{output_filename}'")

    plt.show()

if __name__ == '__main__':
    analyze_rtt()
