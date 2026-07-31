namespace UnityEngine
{
    public class MonoBehaviour
    {
        public T GetComponent<T>() where T : class, new()
        {
            return new T();
        }

        public static T FindObjectOfType<T>() where T : class, new()
        {
            return new T();
        }
    }

    public class Rigidbody
    {
        public Vector3 position;
    }

    public struct Vector3
    {
        public float x, y, z;

        public static Vector3 operator +(Vector3 a, Vector3 b)
        {
            return new Vector3 { x = a.x + b.x, y = a.y + b.y, z = a.z + b.z };
        }
    }

    public class RequireComponent : System.Attribute
    {
        public RequireComponent(System.Type type) { }
    }
}
