import os
import sys
import joblib
import pandas
import numpy as np
import warnings
import random
import logging

from flask import Flask, request, jsonify

MODELS_DIR = "/models"
ARCH_LABEL = "kubernetes.io/arch"
INSTANCE_TYPE_LABEL = "node.kubernetes.io/instance-type"
CONTROL_PLANE_LABEL = "node-role.kubernetes.io/control-plane"
HOSTNAME_LABEL = "kubernetes.io/hostname"

app = Flask(__name__)
app.logger.setLevel(logging.DEBUG)
models = {}


def top_level_slicer(X, indices):
    """
    This function is required by joblib to load the sklearn pipeline.
    """
    return X[:, indices]


def load_models(app_instance):
    """
    Loads all model pipelines from the models directory.
    This function should be called once at application startup.
    """
    # Joblib expects top_level_slicer to be in the __main__ module's namespace.
    # This is a required patch when running under a web server like Gunicorn.
    main_module = sys.modules["__main__"]
    setattr(main_module, "top_level_slicer", top_level_slicer)

    app_instance.logger.info(f"Loading models from {MODELS_DIR}...")
    if not os.path.exists(MODELS_DIR):
        app_instance.logger.warning(
            f"Models directory not found at {MODELS_DIR}. No models will be loaded."
        )
        return

    for filename in os.listdir(MODELS_DIR):
        if not filename.endswith(".joblib"):
            continue

        model_name = filename.replace(".joblib", "").replace("lasso_model_", "")
        model_path = os.path.join(MODELS_DIR, filename)

        try:
            pipeline = joblib.load(model_path)
            input_features = pipeline.named_steps[
                "preprocessor"
            ].feature_names_in_.tolist()
            models[model_name] = {"pipeline": pipeline, "features": input_features}
            app_instance.logger.info(
                f"Loaded model '{model_name}' expecting {len(input_features)} features."
            )
        except Exception as e:
            app_instance.logger.error(
                f"Failed to load or introspect model {model_name}: {e}"
            )


def _prepare_dataframe(feature_matrix, model_features=None):
    """
    Prepares a pandas DataFrame from the raw feature matrix.
    - Filters out control plane nodes.
    - Extracts instance identifiers (instance type or hostname).
    - Filters columns if model_features are provided.

    Returns the DataFrame and the list of original instance identifiers.
    """
    # Filter out control plane nodes first
    compute_nodes = [node for node in feature_matrix if CONTROL_PLANE_LABEL not in node]

    # 2. Determine the identifier for each node (instance type > hostname > index)
    identifiers = []
    for i, node in enumerate(compute_nodes):
        if INSTANCE_TYPE_LABEL in node:
            identifiers.append(node[INSTANCE_TYPE_LABEL])
        elif HOSTNAME_LABEL in node:
            identifiers.append(node[HOSTNAME_LABEL])
        else:
            identifiers.append(i)  # Fallback to index if no identifier is found

    # 3. Filter the features for the DataFrame
    if model_features:
        # For model-based prediction, keep only the features the model was trained on
        df_data = [
            {k: v for k, v in node.items() if k in model_features}
            for node in compute_nodes
        ]
    else:
        # For random prediction, we don't need to filter columns
        df_data = compute_nodes

    df = pandas.DataFrame(df_data, index=identifiers)
    return df, compute_nodes, identifiers


def _predict_with_model(model_name, directionality, feature_matrix):
    """
    Performs prediction using a specified ML model.
    """
    model_info = models[model_name]
    pipeline = model_info["pipeline"]
    model_features = model_info["features"]

    # Prepare the DataFrame with the specific features required by the model
    input_df, compute_nodes, _ = _prepare_dataframe(feature_matrix, model_features)

    # Validate DataFrame
    if input_df.isnull().values.any():
        missing_cols = input_df.columns[input_df.isnull().any()].tolist()
        return {
            "error": "Input data is missing required feature values.",
            "missing_features": missing_cols,
        }, 400

    # Suppress sklearn warnings and get scores
    with warnings.catch_warnings():
        warnings.filterwarnings(
            "ignore", message="Found unknown categories.*", category=UserWarning
        )
        scores = pipeline.predict(input_df)

    # 4. Find the best score based on directionality
    if directionality == "maximize":
        best_index = np.argmax(scores)
    elif directionality == "minimize":
        best_index = np.argmin(scores)
    else:
        return {
            "error": "Invalid 'directionality'. Choose 'minimize' or 'maximize'"
        }, 400

    # Construct and return the result
    selected_node_features = compute_nodes[best_index]
    result = {
        "selected_instance": selected_node_features,
        "instance": str(input_df.index[best_index]),
        "instance-selector": (
            INSTANCE_TYPE_LABEL
            if INSTANCE_TYPE_LABEL in selected_node_features
            else HOSTNAME_LABEL
        ),
        "arch": selected_node_features.get(ARCH_LABEL, "unknown"),
        # Don't return for now - doesn't serialize right
        "score": None,
    }
    return result, 200


def _choose_randomly(feature_matrix):
    """
    Performs prediction by selecting a random node.
    """
    # 1. Prepare DataFrame to get computable nodes and identifiers
    _, compute_nodes, identifiers = _prepare_dataframe(feature_matrix)

    if not compute_nodes:
        return {"error": "No computable nodes available for random selection."}, 400

    # 2. Select a random index
    chosen_index = random.randrange(len(compute_nodes))

    # 3. Construct and return the result
    selected_node_features = compute_nodes[chosen_index]

    result = {
        "selected_instance": selected_node_features,
        "instance": identifiers[chosen_index],
        "instance-selector": (
            INSTANCE_TYPE_LABEL
            if INSTANCE_TYPE_LABEL in selected_node_features
            else HOSTNAME_LABEL
        ),
        "arch": selected_node_features.get(ARCH_LABEL, "unknown"),
        "score": None,  # No score for random selection
    }
    return result, 200


@app.route("/predict", methods=["POST"])
def predict():
    """
    Web endpoint to handle prediction requests.
    Routes the request to the appropriate logic (model-based or random).
    """
    data = request.get_json()
    if not data:
        return jsonify({"error": "Invalid JSON input"}), 400

    # 1. Extract and validate inputs
    metric_name = data.get("metric_name")
    directionality = data.get("directionality", "maximize")
    feature_matrix = data.get("features")

    if not all([metric_name, feature_matrix]):
        return (
            jsonify({"error": "Missing 'metric_name' or 'features' in the request"}),
            400,
        )

    # 2. Route to the correct prediction function
    if metric_name == "random":
        app.logger.info("Routing to random prediction logic.")
        result, status_code = _choose_randomly(feature_matrix)
    elif metric_name in models:
        app.logger.info(f"Routing to model-based prediction for metric: {metric_name}")
        result, status_code = _predict_with_model(
            metric_name, directionality, feature_matrix
        )
    else:
        app.logger.error(f"Model '{metric_name}' not found.")
        available = list(models.keys()) + ["random"]
        return (
            jsonify(
                {
                    "error": f"Model '{metric_name}' not found. Available models: {available}"
                }
            ),
            404,
        )

    app.logger.error(result)
    return jsonify(result), status_code


if __name__ == "__main__":
    load_models(app)
    app.run(host="0.0.0.0", port=5000)

else:
    # Load models when running with a production server like Gunicorn
    load_models(app)
