use clap::Parser;
use grepmesh::config::AppConfig;
use grepmesh::server::run_server;
use std::path::PathBuf;

#[derive(Parser, Debug)]
struct Args {
    #[arg(long)]
    config: PathBuf,
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .with_target(false)
        .compact()
        .init();

    let args = Args::parse();
    let config = AppConfig::from_path(&args.config)?;
    run_server(config).await
}
