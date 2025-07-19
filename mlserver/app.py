import os
import joblib
import pandas 
from flask import Flask, request, jsonify
import numpy as np
import sys

app = Flask(__name__)

MODELS_DIR = '/models'
models = {}

# We need this same function to load models
def top_level_slicer(X, indices):
    return X[:, indices]


def load_models():
    """
    Loads all model pipelines from the models directory and derives the
    required input features directly from the pipeline object.
    """
    # Joblib expects top_level_slicer to be here
    main_module = sys.modules['__main__']
    setattr(main_module, 'top_level_slicer', top_level_slicer)
    
    app.logger.info(f"Loading models from {MODELS_DIR}...")
    for filename in os.listdir(MODELS_DIR):
        if filename.endswith('.joblib'):

            # E.g., model goes from lasso_model_hpcg_fom.joblib to hpcg_fom
            model_name = filename.replace('.joblib', '').replace('lasso_model_', '')
            model_path = os.path.join(MODELS_DIR, filename)

            try:
                pipeline = joblib.load(model_path)
                
                # Access the preprocessor step and get the input feature names.
                input_features = pipeline.named_steps['preprocessor'].feature_names_in_.tolist()
                models[model_name] = {
                    'pipeline': pipeline,
                    'features': input_features
                }
                app.logger.info(f"Successfully loaded model '{model_name}' which expects {len(input_features)} input features.")

            except Exception as e:
                app.logger.error(f"Failed to load or introspect model {model_name}: {e}")


# Load models on startup
with app.app_context():
    load_models()

@app.route('/predict', methods=['POST'])
def predict():
    """
    Receives a metric name and a matrix of raw feature vectors,
    and returns the instance that is predicted to perform best.
    """
    data = request.get_json()

    if not data:
        return jsonify({"error": "Invalid JSON input"}), 400

    metric_name = data.get('metric_name')
    directionality = data.get('directionality', 'maximize')
    feature_matrix = data.get('features')
    print(f"metric name: {metric_name}")
    print(f"directionality: {directionality}")
    print(f"feature matrix: {len(feature_matrix)}")

    if not all([metric_name, feature_matrix]):
        return jsonify({"error": "Missing 'metric_name' or 'features' in the request"}), 400

    if metric_name not in models:
        return jsonify({"error": f"Model '{metric_name}' not found. Available models: {list(models.keys())}"}), 404

    model_info = models[metric_name]
    pipeline = model_info['pipeline']

    # Assemble into matrix. We get the instance type from the name
    # or if not provided, we just return the index
    feature_matrix = [x for x in feature_matrix if 'node-role.kubernetes.io/control-plane' not in x]
    instances = [x['node.kubernetes.io/instance-type'] if "node.kubernetes.io/instance-type" in x else i for i,x in enumerate(feature_matrix)]
    feature_matrix = [{k:v for k,v in x.items() if k in model_info['features']} for x in feature_matrix]

    # Filter down to those required for the model
    input_df = pandas.DataFrame(feature_matrix)
    input_df.index = instances

    # Check for missing values for columns that the model absolutely needs.
    if input_df.isnull().values.any():
        missing_cols = input_df.columns[input_df.isnull().any()].tolist()
        return jsonify({
            "error": "Input data is missing required feature values.",
            "missing_features": missing_cols
        }), 400

    # Pass the DataFrame to the pipeline for prediction.
    scores = pipeline.predict(input_df)

    # Select the best instance based on the specified direction.
    if directionality == 'maximize':
        best_index = np.argmax(scores)
    elif directionality == 'minimize':
        best_index = np.argmin(scores)
    else:
        return jsonify({"error": "Invalid 'directionality'. Choose 'minimize' or 'maximize'"}), 400

    selected_instance = feature_matrix[best_index]

    # Not sure we need this, not returning for now
    # best_score = scores[best_index]
    arch = input_df.loc[best_index, "kubernetes.io/arch"]

    instance_id = input_df.index[best_index]
    try:
        instance_id = float(instance_id)
    except:
        pass
    result = {
        "selected_instance": selected_instance,
        "instance_index": int(best_index),
        "instance": instance_id,
        "arch": arch,
    }
    print(result)
    return jsonify(result)


# Assume we run on localhost in the sidecar
if __name__ == '__main__':
    app.run(host='0.0.0.0', port=5000)
